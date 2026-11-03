## Context

`docker-desk-workspace` built the desk for one daemon behind a local unix socket and refused
everything else at launch (its decision D5: "a context left pointing at a remote host is one of
the likeliest ways to delete things on the wrong machine"). The owner now asks for the
opposite: reach a remote daemon "through ssh or any other communication protocols", and, as a
second task, "applying theme". See `proposal.md` for scope and `specs/` for the contract; this
document covers how and why. Decisions here are numbered **R** (remote) and **T** (themes) so
that they do not collide with the first round's D and B entries; `docs/DECISIONS.md` carries
them in the same format as the rest, and `docs/JOURNAL.md` tells how they met reality.

Facts that shaped the design, observed before or while building:

- The desk is not a one-shot command. With twelve running containers it holds an event stream,
  a log stream, twelve statistics streams and a small pool of request connections, and opens
  one more (hijacked) for a shell. The first-round cap is 32 statistics streams.
- OpenSSH is installed here (client and server, 9.6). A throwaway, unprivileged `sshd` on a
  loopback port can be started by the test suite, so the SSH path can be proven with the real
  client and the real server.
- Things about OpenSSH learned by trying, not from the manual:
  - the client reports every failure to open the far end of a socket forward with the same
    words (`open failed: connect failed: open failed`) — missing socket, permission denied and
    a server that forbids forwarding are indistinguishable from the client;
  - `AllowTcpForwarding no` and `PermitOpen none` on the server also forbid forwarding to
    *unix sockets*, not only `AllowStreamLocalForwarding no` and `DisableForwarding`;
  - the client leaves its listening socket file behind when it is terminated;
  - `sshd` serves each connection from a child in its own session, which survives the
    listener.
- The Moby client reports every failure to connect with one sentence and does not expose the
  cause (`ECONNREFUSED`, a DNS error) to `errors.Is`.
- The constraint from the first round stands: nothing in development may mutate a real daemon.

## Goals / Non-Goals

**Goals:**

- A context or URL that works with the Docker CLI works with the desk: same URL syntax, same
  precedence, same TLS rules, same context store.
- The remote transports are invisible to everything above the engine layer: the planner, the
  jobs, the streams and the shell do not know or care.
- A remote daemon is never acted on without the operator having been told, in the dialog
  itself, that it is remote.
- Nothing is ever asked on the terminal while the desk owns it.
- Every remote path is proven end to end with real protocol implementations (the real `ssh`
  and `sshd`, real TLS, the real `docker` CLI writing the context store) against the fake
  daemon.
- Themes cannot change anything but colour, and the default look does not change.

**Non-Goals:**

- Switching daemon inside the desk; several daemons at once.
- Windows named pipes; `http(s)://` endpoints.
- An SSH implementation, credential storage, or re-authentication from inside the desk.
- Theme files, user-defined palettes, a configuration file.
- Painting the terminal background.

## Decisions

### R1. Remote endpoints are accepted; visibility replaces refusal

`ssh://` and `tcp://` endpoints connect. D5 is reversed because the owner asked for it. What
D5 protected against — acting on a machine you did not mean — is now handled by saying so:
a tag next to the endpoint in the header, never dropped (R11), and repeated inside every
confirmation, form and picker, where the decision is made.

- *An opt-in flag (`--remote`) on top of the context.* A context or `--host` is already an
  explicit choice of daemon; a flag required every time becomes part of the alias and protects
  nothing.
- *Typed confirmation for every mutation on a remote daemon.* It would make the common case —
  restart a container on the server you are looking at — as heavy as a deletion, and D12's
  argument against unbearable controls applies.

Cost: the guard is weaker than a refusal. It is as strong as the operator reading the dialog,
which is what every other rule of the desk already rests on.

### R2. SSH: one forwarded socket, not `docker system dial-stdio` per connection

The Docker CLI reaches an `ssh://` daemon by running `ssh host docker system dial-stdio` once
per HTTP connection. The desk instead runs **one** `ssh -N -L <private local socket>:<remote
socket>` and points the unchanged unix-socket client at the local end.

Why not the CLI's way: every connection would be a new ssh process, a new key exchange and a
new authentication. The desk opens dozens (see Context). `sshd`'s defaults drop connections
beyond ten being set up at once (`MaxStartups 10:30:100`), and the usual cure — connection
sharing — is capped at ten sessions per connection (`MaxSessions 10`). The CLI gets away with
it because a command opens one or two connections; the desk would be flaky at start-up and
slow at every action. Forwarded socket connections are channels, not sessions: one tunnel
carried 40 concurrent statistics streams plus the event stream and a shell in the test.

It also changes what the remote machine needs: no `docker` CLI there, only the daemon's socket.

Costs, all stated to the operator when they bite (R8):

- The SSH server must permit forwarding to unix sockets. It does by default; a hardened server
  with `AllowStreamLocalForwarding no`, `AllowTcpForwarding no`, `PermitOpen none` or
  `DisableForwarding yes` refuses, where the CLI's way would still work.
- The remote socket's path must be known. Default `/var/run/docker.sock`; anything else
  (rootless: `/run/user/<uid>/docker.sock`) goes in the URL path, which is the Docker CLI's own
  syntax for it. The remote user's Docker context is not consulted.
- OpenSSH 6.7 (2014) or newer on both sides.

### R3. The system's `ssh`, not an SSH library

`golang.org/x/crypto/ssh` could open the same channels from inside the binary. It would also
have to re-implement what the operator has already configured and trusts: `~/.ssh/config`
(aliases, `ProxyJump`, per-host identities), the agent, `known_hosts` handling, certificates,
hardware keys, GSSAPI. Getting host-key verification subtly wrong in a tool that deletes
things on servers is the worst place to have a private implementation. The CLI made the same
choice. Cost: `ssh://` endpoints need the OpenSSH client at run time (unix and tcp endpoints
still need nothing). `DOCKER_DESK_SSH` names another program or leading arguments.

What reaches ssh's command line is held to a list of characters that mean nothing to ssh, to a
shell or to a terminal — letters, digits and a few marks per field — and the destination is
passed after `--`. This is narrower than what ssh accepts, on purpose: a context file is not
always the operator's own writing, and ssh *expands* `${VAR}` and `%`-tokens in the paths of a
forwarding (found by the review: a socket path of `/${AWS_SECRET_ACCESS_KEY}` reached the
server expanded). Anything unusual belongs in `~/.ssh/config`, under an alias.

### R4. ssh asks before the desk opens, and never after

The tunnel is started before the alternate screen is entered, attached to the operator's
terminal. A passphrase, a password, a host-key question — ssh asks them itself, on the ordinary
screen, and the desk opens when the local socket answers. (The CLI's way cannot offer this
inside a full-screen program at all.) When not started from a terminal, and for every
reconnection, ssh runs with `BatchMode=yes` and fails instead of asking.

`BatchMode` alone turned out not to be enough (found by the review, which reproduced it): ssh
does not pass it to the ssh it starts for a jump host, and a `ProxyCommand` can open the
terminal by itself. A non-interactive run is therefore also started in a session of its own —
with no controlling terminal there is nothing to ask on — and with
`SSH_ASKPASS_REQUIRE=never`, so that it does not try a graphical prompt instead.

Cost: a login that needs a typed answer cannot be re-established after a drop. The desk stays
up, disconnected, says why ("Permission denied"), and can be restarted. An agent removes the
limit. Re-authenticating from inside (suspending the screen as the shell does) is possible
later and was left out.

### R5. The tunnel is the desk's own, supervised, and private

- `ControlMaster=no`, `ControlPath=none`: the tunnel never joins the operator's shared
  connection, whose forwarding would outlive the desk.
- `ServerAliveInterval=10`, `ServerAliveCountMax=3`: a dead link is noticed in about half a
  minute; ssh exits; a supervisor starts it again with a pause growing from 1 s to 15 s.
- The local socket lives in a fresh `0700` directory and is created `0600`
  (`StreamLocalBindMask=0177`): whoever can connect to it controls the remote daemon. Its path
  stays the same across restarts (`StreamLocalBindUnlink=yes`).
- On quit, launch failure, Ctrl+C during launch and a hang-up (the terminal window being
  closed) at any time, ssh is ended and the directory removed. On Linux the kernel is also
  asked to end ssh if the desk dies without the chance to (`SIGKILL`): the "parent death"
  signal, set from a thread that lives as long as the child.
- `ForkAfterAuthentication=no`: an ssh that the operator's configuration sends to the
  background after logging in would look like one that had exited, and would be left running.
  The option is newer (OpenSSH 8.7) than the rest of the command line; a client that rejects
  it is run without it, and an ssh that exits with status 0 before the tunnel is up is
  reported as having gone to the background.
- `ExitOnForwardFailure` is deliberately *not* set: a forward in the operator's own
  `~/.ssh/config` that cannot bind (the port is in use by their interactive session) would
  then stop the desk from starting. The desk's own forward binds a path nobody else has; when
  ssh says it could not, the launch fails at once with ssh's words.

### R6. TCP and TLS follow the Docker CLI's rules; plain TCP is allowed and labelled

`DOCKER_TLS_VERIFY` (verified, `ca.pem` required), `DOCKER_TLS` (encrypted, unverified),
`DOCKER_CERT_PATH` (default: the config directory) apply to an endpoint named directly; a named
context uses `contexts/tls/<id>/docker/{ca,cert,key}.pem` and `SkipTLSVerify`, and ignores the
environment. A port not given means 2375 — with TLS too, because that is where the CLI dials
— and the port in use is shown. The "default" context follows `DOCKER_HOST`, and becomes
`tcp://localhost:2376` when TLS is asked for and no host is named: odd rules, but the CLI's,
and the desk must not look at a different daemon than `docker` does (both found by the review
and confirmed against the CLI). A context storing *any* TLS file is TLS. These are
re-implemented next to the context resolution
(first-round D4), and checked against the real CLI: the test has `docker context create` write
the store and `docker version` use it, then the desk must reach the same fake daemon.

Plain TCP is not refused: the CLI allows it and it is how people reach daemons across a VPN or
a `socat` relay. It is labelled `tcp, NO TLS` in the danger colour in the header and in every
confirmation. TLS without verification is labelled `tls, UNVERIFIED` the same way.

- *Refuse plain TCP unless a flag is given.* Rejected for the reason in R1.

### R7. `--host` and `--context`, with the CLI's precedence

Naming a remote daemon should not require exporting a variable. The flags are the CLI's, with
its precedence (`--context` beats `DOCKER_HOST`; both flags together are an error). This
relaxes the first-round remark (B20) that the desk has no second way to choose a daemon: these
are the Docker CLI's own two ways, still fixed for the session and shown with their source.

### R8. A remote failure is explained for what it is

- TCP: the port is dialled once before the client is used, because the client's single
  sentence hides the cause. Skipped when the environment routes the address through a proxy.
- TLS: the handshake errors are mapped — wrong CA, wrong name, TLS to a plain port, plain to a
  TLS port, client certificate missing or refused.
- SSH, before the tunnel is up: ssh's own last lines, minus notices, with a hint for the two
  common cases.
- SSH, tunnel up but the socket unreachable: since the client cannot tell (Context), the remote
  machine is asked over a second, non-interactive connection (`sh -c 'test …'`): missing, not a
  socket, not permitted, or hidden in a directory the user may not look into. When the socket
  is there and openable, two causes remain that cannot be told apart — nothing listening, or
  the server forbids forwarding — and both are named. When the question cannot be asked, all
  possibilities are listed.
- SSH, tunnel up and no answer in time: said as that. Silence is not a refusal, and the first
  version explained it as one.
- A certificate file that cannot be used is the client's own set-up failing, and is not
  reported under "the daemon is not reachable".
- Error text from the client library quotes the address it dialled — for SSH, the tunnel's
  local socket. Messages are rewritten to name the endpoint the operator knows.

### R9. The test guard covers the new ways in

Under the guard, a read-write engine may be opened only on a unix socket under the temporary
directory or on a TCP address the fake engine itself is listening on; an SSH tunnel may be
opened only to a remote socket under the temporary directory (the tunnel's local end always is,
so without this the engine's check could be walked around). The switch moved to its own small
package because two layers obey it.

### R10. How it is proven

Layered as in the first round. New: engine tests over plain TCP and mutual TLS (including the
hijacked exec, which the client dials by another path); the tunnel's supervisor against a
stand-in ssh that fails the ways ssh fails; the same tunnel with the real ssh and a real sshd;
the launch sequence in process (`--check`) for every transport and failure; the real binary on
a pseudo-terminal over TCP, TLS and SSH — including a passphrase typed before the desk opens,
the link dropping and returning, Ctrl+C during the connection, and the desk being killed.

### R11. Nothing that identifies the daemon is cut off

Found by the first end-to-end run over SSH: the endpoint was long, and the dialog's single
"daemon" line was truncated on the right — which is where the context and the remote tag
were. In a dialog the endpoint, the context and the tag each go beside what is there when they
fit and onto their own line when they do not; one longer than a line is continued. In the
header the *middle* of the endpoint gives way — its beginning says which machine, its end which
socket — after everything else that can.

The review then found the same class of fault one level up: with the extra lines, some
confirmations no longer fitted 80×24, and the prompt and input were drawn off screen (one did
so before this change). A confirmation is now laid out so that it cannot: what is acted on,
the daemon, and the prompt, input and keys never move; the effects, warnings and listed
objects between them scroll together when they do not fit.

### R13. An independent review, before calling it done

As in the first round: the new ways in were given to a separate reviewer with ten rules and no
account of why the code was believed to obey them. It found sixteen things, two of them
serious — a prompt from a jump host's ssh underneath the running desk, and environment
variables expanded by ssh into a path sent to the server. The table of findings and outcomes
is in `docs/DECISIONS.md`, R13.

### R12. "Remote" is decided by the address

`localhost` and loopback addresses are not called remote (the tag then names only the
transport); everything else is. A name that merely resolves to this machine is called remote:
the tag errs on the side of caution, and the header also shows the engine's own host name.

### T1. A theme is a palette of meanings

Seven colours — accent, dim, good, warn, bad, unfocused border, unfocused title — plus an
optional pair for the highlighted row. Every style is built from them in one function; weight,
underline and reverse video are the same in all themes. Layout, glyphs and wording are out of a
theme's reach by construction, and a test compares every frame across themes.

The first round listed themes as a non-goal; the owner now asks for them. "Applying theme" was
read as: give the desk selectable colour themes and apply one. Theme *files* remain out: there
is no configuration file, and a palette that can be edited is a palette that can make
"destructive" green.

### T2. The default look does not change

`dark` is the original seven colours, pinned by a test. With no setting the desk picks `dark`,
or `light` when the terminal reports a light background — the one case where the original
palette was hard to read.

### T3. `--theme`, `DOCKER_DESK_THEME`, and `T`

A flag and a variable select the theme; `T` cycles through them in the running desk so that a
theme can be chosen by looking. Nothing is stored — the status line says how to keep the theme.
An unknown name is an error listing the known ones, not a silent fall-back. `T` is handled
after dialogs and the filter have had the key, like every other global key.

### T4. Themes are checked for contrast, and do not paint the background

Named palettes assume a terminal already using that scheme. Each text colour is tested at
≥ 3:1 against that scheme's background, the highlighted row at ≥ 4.5:1. Where a scheme's own
colour failed for text that is read constantly (Dracula's comment colour for hints,
Solarized's green and yellow on its light background, several border colours), a lighter or
darker member of the same family is used.

### T5. No colour still means no colour

`NO_COLOR` wins over any theme; every theme then draws the same frame. `mono` is the same thing
by choice. (Found while testing: with no colour the renderer emits no attributes at all — not
even reverse video — so the highlighted row is marked by its `▸` glyph alone, as it always was.)

## Risks / Trade-offs

- **Acting on the wrong machine** (the risk D5 removed) → the remote tag in the header and in
  every confirmation; `--read-only`; nothing else. Accepted at the owner's request.
- **Hardened SSH servers refuse socket forwarding** → explained at launch with the settings to
  look at; the Docker CLI's `dial-stdio` way is not offered as a fallback (R2).
- **Passphrase or password logins cannot reconnect** → stated in the status line and the
  README; an agent removes it.
- **An unencrypted or unverified daemon is one environment variable away** → labelled in the
  danger colour everywhere the daemon is named.
- **A desk killed with `SIGKILL` on macOS leaves its ssh running** → Linux only can prevent it;
  documented. The socket is private either way.
- **Re-implemented TLS and context rules can drift from Docker's** (as D4) → cross-checked
  against the real CLI in the suite.
- **The mutating paths have still never run against a real daemon**, now including a real
  remote one. The chain of evidence is the first round's, extended by real transports.

## Migration Plan

No data to migrate. Behaviour change: a context pointing at `ssh://` or `tcp://` now connects
instead of refusing. Rollback is reverting the change; nothing is stored outside the process.

## Open Questions

- Should a dropped SSH link that needs an answer be re-established from inside the desk, by
  handing the terminal to ssh the way the shell is handed it?
- Should the theme be remembered between runs? That needs a configuration file, which the desk
  has so far done without.
