## Why

Docker Desk opens only on a daemon behind a local unix socket; a context that points anywhere
else is refused at launch. The containers people most need to watch and act on are often on
another machine, and today that means going back to `ssh host docker ps`, `docker -H … logs -f`
and the five-command dance the desk was built to replace. The desk should open on a remote
daemon the same way the Docker CLI reaches one — over SSH, or over TCP with or without TLS —
without weakening anything that makes it safe to act from.

Separately, the desk's colours are fixed, chosen for a dark terminal. On a light background
several of them are hard to read, and people who run a terminal colour scheme get a desk that
ignores it. The desk should have themes.

## What Changes

- **Remote daemons.** The endpoint may be `ssh://[user@]host[:port][/path/to/docker.sock]` or
  `tcp://host[:port]`, as well as `unix://…`. Which endpoint is used is still decided exactly as
  the Docker CLI decides it.
  - **SSH** goes through the operator's own `ssh` client — its configuration, keys, agent, jump
    hosts and known hosts — as one connection that forwards a private local socket to the
    daemon's socket on the remote machine. Whatever `ssh` needs to ask (a passphrase, a
    password, an unknown host key) is asked on the ordinary screen before the workspace opens,
    and never afterwards. If the connection drops, it is re-established without questions.
  - **TCP** uses TLS when the Docker CLI would: `DOCKER_TLS_VERIFY` / `DOCKER_TLS` /
    `DOCKER_CERT_PATH` for an endpoint named directly, the context's stored certificates for a
    named context. Without those it is plain TCP — allowed, as in the CLI, and labelled as not
    encrypted wherever the daemon is named.
- **`--host` / `-H` and `--context` / `-c`** launch flags, with the Docker CLI's precedence,
  so that a remote daemon can be named without changing the environment.
- **The operator is told the daemon is remote, and how safely it is reached**: a tag next to
  the endpoint in the header (`REMOTE ssh`, `REMOTE tls`, `REMOTE tls, UNVERIFIED`,
  `REMOTE tcp, NO TLS`) that is never dropped or cut off, repeated in every confirmation, form
  and picker, in the help, and by `--check`.
- **Remote failures are explained before the workspace opens**, each for what it is: `ssh`'s
  own words when it cannot log in; no socket at that path on the remote machine, not permitted
  to open it, or the SSH server refusing to forward; a TCP port that refuses, a name that does
  not resolve; TLS asked of a port that does not speak it and the reverse; a certificate from
  the wrong authority; a client certificate missing or refused.
- **A dropped SSH link is distinguishable from a stopped daemon**: the desk shows the
  disconnected state as before and adds why the link is down.
- **A confirmation always fits the terminal.** Naming a remote daemon in full takes more lines,
  and at 80×24 some confirmations no longer fitted — one did not before this change. What is
  acted on, the daemon, the prompt, the input and the keys now always stay on screen; the
  effects, warnings and listed objects between them scroll. (This changes dialogs introduced
  by `docker-desk-workspace`.)
- **Closing the terminal ends the desk cleanly**, like quitting it.
- **Colour themes.** Built-in palettes — `dark` (the present colours), `light`, `ansi` (the
  terminal's own 16 colours), `dracula`, `nord`, `gruvbox`, `solarized-dark`,
  `solarized-light`, `catppuccin`, `mono` — chosen with `--theme` or `DOCKER_DESK_THEME`, or
  picked automatically (`dark` or `light`) from the terminal's background. `T` cycles through
  them while the desk is open. A theme changes colour and nothing else.
- **BREAKING (behaviour):** a Docker context pointing at `ssh://` or `tcp://` used to make the
  program refuse to start; it now connects. Anyone who relied on that refusal as a guard
  against acting on a remote machine no longer has it — the remote tag replaces it.

### Explicitly out of scope

- More than one daemon per session, or switching daemon while running (unchanged).
- Windows named pipes (`npipe://`), `fd://`, `http://`, `https://` endpoints: refused with a
  message naming the supported schemes.
- TLS flags on the command line (`--tlsverify`, `--tlscacert`, …): the environment variables
  and context store cover it.
- Passphrase-protected TLS client keys (the Docker CLI does not support them either).
- An SSH implementation of its own, or reaching the daemon by running `docker` on the remote
  machine (see design, R2 and R3).
- Asking for SSH credentials again from inside the workspace after a dropped link.
- User-defined themes, theme files, or any configuration file.
- Painting the terminal's background.

## Capabilities

### New Capabilities

- `colour-themes`: the built-in palettes, how one is selected and switched, what a theme may
  and may not change, and behaviour without colour.

### Modified Capabilities

- `daemon-connection`: endpoints may be remote (SSH, TCP, TCP+TLS); `--host` and `--context`;
  the "local daemons only" requirement is removed; the unreachable-daemon, daemon-identity and
  connection-loss requirements gain their remote cases.

## Impact

- **Code**: `cmd/docker-desk` (flags, launch sequence, hang-up), `internal/dockerctx`
  (resolution with TLS, endpoint parsing, failure classification), new `internal/sshtunnel`
  (the supervised ssh process), `internal/engine` (TLS client, bounded exec attach, endpoint
  names in errors), `internal/ui` (themes, remote tag, link status, confirmation layout),
  `internal/live` (a longer reconnect timeout for remote daemons), new `internal/testguard`,
  `internal/fakeengine` (TCP and TLS listeners), new `internal/sshtest` (a throwaway sshd for
  tests), `internal/ptyscreen` (a harness environment that names no real daemon).
- **Dependencies**: no new Go modules. At run time, `ssh://` endpoints need the OpenSSH client
  (6.7 or newer) on this machine and an SSH server on the other that permits forwarding to unix
  sockets; nothing is needed on the remote machine beyond Docker itself — not the `docker` CLI.
- **Systems**: the program now opens outbound SSH and TCP/TLS connections when its endpoint
  says so, and starts one `ssh` child process for an SSH endpoint.
- **Safety**: the first-round decision that refused remote endpoints (D5) is reversed at the
  owner's request. The wrong-machine risk it removed is addressed by visibility instead.
- **Tests**: the suite still touches no real daemon. The guard that enforces this is extended
  to TCP addresses and SSH tunnels.
- **Ordering**: this change modifies a capability introduced by `docker-desk-workspace`, which
  is complete but not yet archived. Archive that change first, then this one.
