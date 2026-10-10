# Docker Desk

One screen for a Docker engine — the one on this machine, or one on another, over SSH or TCP.
Not a table you scroll — a desk: the kinds of things
Docker has on the left, the objects of the kind you picked in the middle with their details
underneath, the engine's own event stream ticking by on the right, and the slow work you sent
off in the corner. All alive at once, all from the keyboard.

```
 Docker Desk  unix:///var/run/docker.sock  ctx default  Engine 29.8.2  on myhost  ● connected
┌─ 1 Kinds ────────┐┏━ ◆ 2 Containers (12) ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┓┌─ 4 Events ─────────── ● live ─┐
│▸ Containers    12│┃  NAME            IMAGE         STATE        CPU      MEM     ┃│06:51:17 ctr kill sig 15 web   │
│  Images        14│┃▸ shop-web-1      nginx:1.27    ● running   0.3%  12.1MiB     ┃│06:51:17 ctr die exit 0 web    │
│  Volumes        7│┃  shop-db-1       postgres:16   ● running   1.2%   140MiB     ┃│06:51:17 net disconnect shop_… │
│  Networks       7│┃  paused-batch    alpine:3.20   ‖ paused                      ┃│06:51:17 ctr stop       web    │
│  Compose        2│┃  shop-worker-1   shop/worker…  ○ exited                      ┃│                               │
│                  │┗━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┛│                               │
│  ● 7 running     │┌─ 3 Details: shop-web-1 ──────── [Info] Logs Stats Inspect ─┐│                               │
│  ‖ 1 paused      ││Name     shop-web-1                                         │└───────────────────────────────┘
│  ○ 4 stopped     ││State    ● running   Up 3 hours                             │┌─ 5 Jobs ─────── ⠙ 1 running ─┐
│                  ││Ports      0.0.0.0:8080 → 80/tcp                            ││▸ ⠙ pull image redis:7     43% │
│                  ││Networks   shop_default  172.18.0.2  gw 172.18.0.1          ││  ✓ stop container web     2ms │
└──────────────────┘└────────────────────────────────────────────────────────────┘└───────────────────────────────┘
 ↑↓ move  / filter  ⏎ actions  s start  t stop  R restart  p pause  u unpause  K kill …   Tab pane  ? keys  q quit
```

Real captures of every screen are in [`docs/screens/`](docs/screens/).

## Install and launch

You need Go 1.26 or newer to build, and a Docker daemon to look at — local or remote.

```sh
make install      # puts docker-desk in $(go env GOPATH)/bin   (or: go install ./cmd/docker-desk)
docker-desk
```

Or without installing: `make build && ./bin/docker-desk`.

Docker Desk needs nothing else at run time — not even the `docker` CLI, on this machine or on
the remote one. The one exception: an `ssh://` daemon is reached through your `ssh` client.

| Command | What it does |
| --- | --- |
| `docker-desk` | open the desk on the daemon your current Docker context points at |
| `docker-desk -H ssh://ops@prod-1` | open it on that daemon instead (`--host`; any `unix://`, `ssh://` or `tcp://` URL) |
| `docker-desk -c staging` | open it on the daemon of that Docker context (`--context`) |
| `docker-desk --read-only` | look, don't touch: every action that changes something, and the shell, is disabled; anything but `GET`/`HEAD` is refused before it is sent |
| `docker-desk --check` | say which daemon would be used, how it is reached and whether it answers, then exit |
| `docker-desk --theme nord` | pick a colour theme (see [Themes](#themes)) |
| `docker-desk --version`, `--help` | |
| `make sandbox` | start a **fake** engine full of containers and open the desk on it — the place to try `remove` and `prune` with nothing at stake |

### Which daemon?

Exactly one, for the whole session, decided the way the Docker CLI decides it: `--context`,
then `--host`, then `DOCKER_HOST`, then `DOCKER_CONTEXT`, then the context chosen with
`docker context use`, then `unix:///var/run/docker.sock`. (`--host` and `--context` together
are refused. As in the CLI, the context named `default` *is* `DOCKER_HOST` when that is set.) The endpoint is on the top line of the screen at all times, and again inside
every confirmation, so you know the blast radius before you touch anything. There is no way to
switch daemon from inside.

| Endpoint | Reached how |
| --- | --- |
| `unix:///path/to/docker.sock` | a socket on this machine |
| `ssh://[user@]host[:port][/path]` | the daemon's socket on another machine, through your `ssh` client |
| `tcp://host[:port]` | the Engine API over TCP — with TLS when configured, otherwise **not encrypted**; port 2375 if none is given |

Anything else (`npipe://`, `fd://`, `http://`) is refused at launch.

`docker-desk --check` shows what would be used without opening anything. (The remote examples
in this README are illustrations: the wording is the program's, the hosts and versions are
made up.)

```
endpoint   ssh://ops@prod-1
context    prod
chosen by  --context flag
via        an SSH tunnel to /var/run/docker.sock on prod-1
engine     28.1.1 on prod-1 (API 1.49, linux/x86_64)
mode       read-write (every change still needs an in-app confirmation)
```

### A daemon on another machine

**It says so.** A daemon that is not this machine's own socket carries a tag next to the
endpoint — in the header, where it is never dropped however narrow the terminal, and inside
every confirmation, form and picker:

| Tag | Meaning |
| --- | --- |
| `[REMOTE ssh]` | another machine, through SSH |
| `[REMOTE tls]` | another machine, TLS, the daemon's certificate verified |
| `[REMOTE tls, UNVERIFIED]` | TLS, but anyone could be answering — shown in the colour of danger |
| `[REMOTE tcp, NO TLS]` | not encrypted, and open to whoever reaches the port — also in that colour |

(Without `REMOTE` when the address is `localhost` or a loopback address.)

**Over SSH.** `ssh://ops@prod-1` means: the daemon behind `/var/run/docker.sock` on `prod-1`,
logged in as `ops`. A daemon listening elsewhere is named by its path, as in the Docker CLI —
a rootless daemon, for instance: `ssh://ops@prod-1/run/user/1000/docker.sock`.

- It is your `ssh`: `~/.ssh/config` (aliases, jump hosts, per-host keys), your agent and your
  `known_hosts` all apply. If `ssh ops@prod-1` works, so does the desk. To use another program
  or extra options: `DOCKER_DESK_SSH="ssh -F $HOME/.ssh/work_config"`.
- Whatever ssh has to ask — a key's passphrase, a password, an unknown host key — it asks on
  the ordinary screen, *before* the desk opens. Nothing is ever asked afterwards, by ssh or by
  the ssh it starts for a jump host: a reconnection is given no terminal to ask on.
- The user, host and socket path in the URL may contain only letters, digits and a few marks
  (`. _ -`, and `/ @ + , =` in a path). Anything else — ssh would expand a `$` or a `%` in the
  path, for one — is refused; put unusual names in `~/.ssh/config` under a host alias and use
  the alias.
- One connection carries everything: lists, logs, the event stream, a statistics stream per
  container, the shell. If it drops, the header says `✗ DISCONNECTED`, the status line says
  why the link is down, and the desk reconnects by itself when it can do so without a question.
  A login that needs a typed passphrase or password cannot be repeated that way — use an agent,
  or restart the desk.
- The remote machine needs Docker and an SSH server that allows forwarding to unix sockets
  (OpenSSH does by default). It does **not** need the `docker` CLI. On this point the desk
  differs from `docker -H ssh://…`, which needs the CLI there but no forwarding; see
  [DECISIONS](docs/DECISIONS.md#r2--ssh-one-forwarded-socket-not-docker-system-dial-stdio-per-connection)
  for why.

**Over TCP.** TLS is used exactly when the Docker CLI would use it:

```sh
# an endpoint named directly: certificates from the environment
DOCKER_TLS_VERIFY=1 DOCKER_CERT_PATH=~/certs/prod docker-desk -H tcp://prod-1:2376

# a context that stores its certificates
docker context create prod --docker "host=tcp://prod-1:2376,ca=ca.pem,cert=cert.pem,key=key.pem"
docker-desk -c prod
```

`DOCKER_CERT_PATH` holds `ca.pem`, and `cert.pem` with `key.pem` if the daemon wants a client
certificate; it defaults to `~/.docker`. `DOCKER_TLS=1` alone encrypts without verifying the
daemon. With none of these the connection is plain TCP: allowed, as in the CLI, and labelled.

Two of the CLI's rules that surprise, and that the desk follows so that it never looks at a
different daemon than `docker` does: a `tcp://` URL without a port means **2375 even with
TLS** (name `:2376` yourself; `--check` shows the port in use), and with `DOCKER_TLS_VERIFY`
set and no endpoint named anywhere, the daemon is `tcp://localhost:2376`.

### When the daemon cannot be reached

The desk tells you plainly — which endpoint, chosen by what, reached how, and why — and never
opens a workspace that would only fail at everything:

```
docker-desk: the Docker daemon is not reachable.

  endpoint   unix:///var/run/docker.sock
  context    default
  chosen by  Docker default (no context configured)
  reason     the socket /var/run/docker.sock does not exist
  hint       Is the Docker daemon installed and running? (e.g. `sudo systemctl start docker`)

The workspace was not opened.
```

For a remote daemon the reason is as specific as it can be made:

```
docker-desk: the Docker daemon is not reachable.

  endpoint   ssh://ops@prod-1
  context    default
  chosen by  --host flag
  via        an SSH tunnel to /var/run/docker.sock on prod-1
  reason     the remote user ops may not open /var/run/docker.sock on prod-1
  hint       The socket usually belongs to the `docker` group: add the remote user to it and log in again.

The workspace was not opened.
```

Likewise: ssh's own words when it cannot log in; no socket at that path; a port that refuses;
a name that does not resolve; TLS asked of a port that does not speak it, and the reverse; a
certificate from the wrong authority; a client certificate missing or refused.

If the daemon goes away while the desk is open — or the network between you and it does — the
header says `✗ DISCONNECTED — retrying`, the last known data stays on screen marked as stale,
the desk keeps answering keys, and everything resumes by itself when the daemon is back.

## The desk

| Pane | Shows |
| --- | --- |
| **1 Kinds** | Containers, Images, Volumes, Networks, Compose — with live counts. Moving the highlight is enough; the list follows. |
| **2 List** | The objects of that kind. Containers show live CPU and memory. The highlight follows the *object* when the list changes underneath it. |
| **3 Details** | The highlighted object: **Info** (readable summary), **Inspect** (the daemon's full JSON), and for containers **Logs** (live follow) and **Stats** (CPU, memory, network, disk — with history). |
| **4 Events** | The engine's event stream as it happens. It is the answer to everything you do — and to everything anyone else does to this daemon. |
| **5 Jobs** | Every change you asked for, running in the background: progress while it runs, the daemon's answer when it ends. |

The focused pane has a heavy border and a `◆` in its title (it does not rely on colour;
`NO_COLOR` is respected, whatever the theme). Wide terminals (≥ 120 columns) get three columns; from 80×24 up you
still get all five panes in a tighter arrangement. Smaller than that, the desk says so and
accepts no key but `q` — nothing can be acted on that is not on screen.

### Themes

| Theme | |
| --- | --- |
| `auto` | the default: `dark`, or `light` when your terminal says its background is light |
| `dark` | the original palette |
| `light` | darker hues that stay readable on a light background |
| `ansi` | only your terminal's own 16 colours, so it follows whatever scheme the terminal has |
| `dracula`, `nord`, `gruvbox`, `solarized-dark`, `solarized-light`, `catppuccin` | for terminals already using that scheme |
| `mono` | no colour at all |

Choose one with `--theme NAME` or `DOCKER_DESK_THEME=NAME`. Inside the desk, `T` switches to
the next one so that you can pick by looking; the status line tells you its name and how to
keep it (nothing is saved for you). A theme changes colours and nothing else: every pane,
border, glyph and word is the same in all of them, red still means "destructive" or "failed",
and the desk never paints your terminal's background.

## Keys

Press `?` inside the desk for the same list.

**Everywhere**

| Key | |
| --- | --- |
| `Tab` / `Shift+Tab` | next / previous pane |
| `1` `2` `3` `4` `5` | focus Kinds / List / Details / Events / Jobs |
| `[` `]` | previous / next Details tab |
| `↑` `↓` or `k` `j`, `PgUp` `PgDn`, `g` `G` | move / page / first / last |
| `/` | filter the list as you type (Enter keeps, Esc undoes; Esc in the list clears) |
| `Enter` | in the List: a menu of what you can do to the highlighted object |
| `r` | refresh every list now |
| `T` | next colour theme |
| `?` | help |
| `q`, `Ctrl+C` | quit (asks first if jobs are running) |

**Acting on things** (List or Details pane focused)

| Kind | Keys |
| --- | --- |
| Containers | `s` start · `t` stop · `R` restart · `p` pause · `u` unpause · `K` kill · `d` remove · `e` shell |
| Images | `P` pull by name · `d` remove · `X` prune |
| Volumes | `n` new · `d` remove · `X` prune |
| Networks | `n` new · `d` remove · `X` prune · `c` connect a container · `D` disconnect a container |
| Compose | `t` stop the whole project · `d` remove the whole project |

**Details**: `←` `→` switch tab · `f` pause/resume following (Logs) · `w` toggle wrapping.
**Events**: scroll back with the movement keys — new events keep being recorded — `G` to
follow again. **Jobs**: `Enter` read a job's full output · `c` cancel a running job · `x` clear
finished ones.

### The shell

`e` on a running container shows the command it will run — by default
`sh -c 'command -v bash >/dev/null 2>&1 && exec bash || exec sh'`, which you can edit — and on
Enter hands your terminal to that process: a proper session, raw keys, resizes forwarded. When
it exits (or the container stops) you are back at the desk exactly where you left it. If the
container has no shell, you are back immediately with the daemon's reason. If the daemon ever
stops answering in the middle of a session, **Ctrl-] three times** ends it locally and brings
the desk back.

## The safety model

Docker Desk changes and deletes real things, so it is deliberate by design rather than by your
being careful.

- **No single keystroke changes anything.** Every action key opens a dialog that names the
  exact object (name and ID), the exact effect in plain words, and the daemon it will be sent
  to. Nothing has been sent at that point.
- **A daemon on another machine is called remote where you decide.** The dialog's "daemon"
  line carries the same tag as the header — `[REMOTE ssh]`, `[REMOTE tcp, NO TLS]` — and is
  never cut short to fit. This is all that stands between a context left pointing at production
  and a change made there; `--read-only` is the stronger guard when you only mean to look.
- **Confirmation scales with what cannot be undone.**
  - start, stop, restart, pause, unpause, kill, connect, disconnect, stop project → press `y`.
    Enter does nothing.
  - anything that deletes — remove, prune, remove project → type the word shown (`remove` or
    `prune`) and press Enter.
  - pull, new volume, new network, shell → you type the name yourself; Enter submits.
- **Only what you press after reading counts.** A dialog ignores `y`, the typed word and Enter
  for its first quarter of a second, and if you press anything else while a confirmation is
  still being prepared, that action is dropped instead of popping up under your fingers. Keys
  typed ahead — or typed for the filter — can never consent to something you have not seen.
  Esc always works at once.
- **A confirmation acts on what it showed.** The object is captured by its daemon ID when the
  dialog opens. If the list reshuffles underneath, the dialog still means the same object.
  Things addressed by a *name* — an image tag, a volume — are re-checked just before the
  request: if the tag has moved to another image or the volume was recreated, nothing is
  removed.
- **Nothing is ever forced.** If the thing is in use, the dialog says by what and offers no way
  to proceed: a running container must be stopped first; an image, volume or network that any
  container references — *running or stopped* — is in use. If something becomes in use after
  you confirm, the daemon refuses and its message is shown.
- **A prune shows exactly what it will destroy, and destroys exactly that.** The dialog lists
  every object; `Tab` switches between the default scope (dangling images / anonymous volumes)
  and "all unused" and recomputes the list. After you confirm, each listed object is deleted
  individually and without force. Docker's own prune endpoints are never called, so nothing
  that appeared after the preview can be swept up with it.
- **Errors never end the session.** A daemon error is shown — in the pane it concerns, in the
  status line, and kept with the job — and the desk stays usable.

Two deliberate differences from the Docker CLI, both on the cautious side:

- `X` on Networks keeps networks used only by *stopped* containers. `docker network prune`
  deletes them.
- "Remove project" removes the project's containers and nothing else. It names the networks
  and volumes that carry the project's label and leaves them where they are. This is not
  `compose down`.

## Compose, honestly

Docker Desk never reads, finds or runs a compose file. A "project" is simply every container
carrying the same `com.docker.compose.project` label — the one Docker Compose already puts on
them. You can stop the group or remove the group. There is no `up`, `down`, `build`, `pull` or
scaling.

## What it does not do

One daemon per session; no switching from inside. No Windows named pipes. No Swarm, no compose
files, no image builds or pushes, no Kubernetes. It does not create or reconfigure containers,
copy files in or out, or show disk-usage accounting (`docker system df`).

Good to know:

- Under a log flood the Logs view is a window onto the newest 5,000 lines (it tells you how
  many older ones scrolled out); very long lines are split at 2 KiB.
- Live statistics are kept for at most 32 running containers at a time; the highlighted one is
  always included, the rest show `n/c`.
- The Info tab shows how many environment variables a container has, not their values; the
  Inspect tab shows everything.
- Quitting cancels running jobs. A request the daemon had already accepted may still complete.
- Over SSH: a server whose `sshd_config` forbids forwarding (`AllowStreamLocalForwarding no`,
  `AllowTcpForwarding no`, `PermitOpen none`, `DisableForwarding yes`) cannot be used, and the
  desk says so at launch. The remote user's own Docker context is not consulted — name the
  socket in the URL if it is not `/var/run/docker.sock`.
- Over SSH, a dropped link is noticed within about half a minute and retried every 1 to 15
  seconds. While it is down the desk shows what it last knew.
- Closing the terminal window ends the desk as `q` does: its `ssh` and the tunnel's socket go
  with it. If the desk is killed outright (`kill -9`) it cannot clean up: on Linux its `ssh` is
  still ended with it, on macOS it keeps running until the link drops or you end it, and an
  empty `docker-desk-ssh-…` directory stays in the temporary directory. The tunnel's socket is
  usable only by you in every case.
- Over TCP, `HTTP_PROXY`/`HTTPS_PROXY` apply, as they do for the Docker CLI — except to the
  shell, whose connection the client library makes directly.
- At 80×24 a confirmation with much to say scrolls its middle — the effects, the warnings, the
  list — and keeps what it acts on, the daemon, the prompt and the input on screen.
- TLS client keys protected by a passphrase are not supported (nor by the Docker CLI).

## Trying it safely

```sh
make sandbox
```

starts `fake-dockerd` — an in-memory engine that speaks the real Docker API, seeded with two
compose projects, containers in every state, a container that logs 20,000 lines a second, one
without a shell, one that takes four seconds to stop, dangling images, unused volumes and an
orphan network — and opens the desk on it. The header shows the sandbox socket. Nothing there
is real; break whatever you like. (The real `docker` CLI works against it too:
`DOCKER_HOST=unix:///tmp/docker-desk-sandbox.sock docker ps`.)

To see how a daemon that is not a local socket is presented, without having one:

```sh
make build
./bin/fake-dockerd -tcp 127.0.0.1:2375 &
./bin/docker-desk -H tcp://127.0.0.1:2375      # header: [tcp, NO TLS]
```

## How it was built, and why

| | |
| --- | --- |
| [`docs/DECISIONS.md`](docs/DECISIONS.md) | every design decision, the alternatives, and the reason — including the ones forced by reality during the build |
| [`docs/JOURNAL.md`](docs/JOURNAL.md) | the build in order: what was made, checked, found and changed |
| [`docs/TESTING.md`](docs/TESTING.md) | how it is proven without touching real containers, the measurements, and what is *not* proven |
| [`openspec/changes/docker-desk-workspace/`](openspec/changes/docker-desk-workspace/) | the spec it was built to: proposal, eleven capability specs, design, tasks |
| [`openspec/changes/remote-daemons-and-themes/`](openspec/changes/remote-daemons-and-themes/) | the second change — remote daemons and themes: proposal, spec deltas, design, tasks |

One thing from TESTING.md belongs here too: by design, **no mutating request from this program
had ever been sent to a real Docker daemon when it was handed over** — the build was required
not to touch real containers. Its requests are encoded by Docker's own client library and were
exercised against a fake engine that Docker's own CLI was used to validate. The first `stop`
you confirm on a real container will be the first one ever. `make sandbox` and `--read-only`
exist so that you can decide when that is.

The same holds for remote daemons, and more so: SSH, TLS and TCP were each driven end to end
with real implementations — your machine's `ssh` against a real `sshd`, real certificates, a
context store written by the real `docker` CLI — but the daemon at the far end was always the
fake one, on this machine. **The desk has never been connected to a Docker daemon on another
computer.** `docker-desk -H ssh://… --check`, then `--read-only`, is the careful way to make
that first contact.

## Layout of the code

```
cmd/docker-desk        the program: resolve the daemon, open the way to it, check it, then open the desk
cmd/fake-dockerd       the sandbox engine
internal/dockerctx     which daemon is meant, how it is reached (socket, SSH, TCP/TLS); why it cannot be
internal/sshtunnel     the one supervised ssh process that carries an ssh:// daemon's connections
internal/engine        the only door to the daemon (Docker's client library behind a narrow interface)
internal/plan          what a mutation will do, decided by pure code before anything is sent
internal/jobs          background jobs and their ledger
internal/live          bounded buffers and stream supervision for logs, stats and events
internal/shell         the interactive exec session and the terminal hand-off
internal/ui            the desk: panes, keys, dialogs
internal/fakeengine    an in-memory Docker Engine speaking the real HTTP API (tests, sandbox)
internal/sshtest       a throwaway sshd on a loopback port (tests)
internal/testguard     the switch that keeps tests away from real daemons
internal/ptyscreen     runs a program on a pseudo-terminal with an emulated screen (tests)
internal/e2e           the real binary, driven like a person would
scripts/               cross-check of the fake engine with the real docker CLI
```

```sh
make test         # everything, against the fake engine only
                  #   (DOCKER_DESK_REQUIRE_SSHD=1 make test — fail instead of skip where sshd cannot run)
make race         # the same under the race detector
make crosscheck   # the real docker CLI against the fake engine
make help         # the rest
```
