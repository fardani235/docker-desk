# Docker Desk

One screen for your local Docker engine. Not a table you scroll — a desk: the kinds of things
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

You need Go 1.26 or newer to build, and a local Docker daemon to look at.

```sh
make install      # puts docker-desk in $(go env GOPATH)/bin   (or: go install ./cmd/docker-desk)
docker-desk
```

Or without installing: `make build && ./bin/docker-desk`.

Docker Desk needs nothing else at run time — not even the `docker` CLI.

| Command | What it does |
| --- | --- |
| `docker-desk` | open the desk on the daemon your current Docker context points at |
| `docker-desk --read-only` | look, don't touch: every action that changes something, and the shell, is disabled; anything but `GET`/`HEAD` is refused before it is sent |
| `docker-desk --check` | say which daemon would be used and whether it answers, then exit |
| `docker-desk --version`, `--help` | |
| `make sandbox` | start a **fake** engine full of containers and open the desk on it — the place to try `remove` and `prune` with nothing at stake |

### Which daemon?

Exactly one, for the whole session: the one your current Docker context resolves to, with the
Docker CLI's own precedence — `DOCKER_HOST`, then `DOCKER_CONTEXT`, then the context chosen
with `docker context use`, then `unix:///var/run/docker.sock`. The endpoint is on the top line
of the screen at all times, and again inside every confirmation, so you know the blast radius
before you touch anything. There is no way to switch daemon from inside.

It only accepts a **local unix socket**. If your context points at `tcp://` or `ssh://`, it
says so and does not start. If the daemon is not reachable it tells you plainly — which
endpoint, chosen by what, and why (socket missing, permission denied, nothing listening, no
answer) — and never opens a workspace that would only fail at everything:

```
docker-desk: the Docker daemon is not reachable.

  endpoint   unix:///var/run/docker.sock
  context    default
  chosen by  Docker default (no context configured)
  reason     the socket /var/run/docker.sock does not exist
  hint       Is the Docker daemon installed and running? (e.g. `sudo systemctl start docker`)

The workspace was not opened.
```

If the daemon goes away while the desk is open, the header says `✗ DISCONNECTED — retrying`,
the last known data stays on screen marked as stale, the desk keeps answering keys, and
everything resumes by itself when the daemon is back.

## The desk

| Pane | Shows |
| --- | --- |
| **1 Kinds** | Containers, Images, Volumes, Networks, Compose — with live counts. Moving the highlight is enough; the list follows. |
| **2 List** | The objects of that kind. Containers show live CPU and memory. The highlight follows the *object* when the list changes underneath it. |
| **3 Details** | The highlighted object: **Info** (readable summary), **Inspect** (the daemon's full JSON), and for containers **Logs** (live follow) and **Stats** (CPU, memory, network, disk — with history). |
| **4 Events** | The engine's event stream as it happens. It is the answer to everything you do — and to everything anyone else does to this daemon. |
| **5 Jobs** | Every change you asked for, running in the background: progress while it runs, the daemon's answer when it ends. |

The focused pane has a heavy border and a `◆` in its title (it does not rely on colour;
`NO_COLOR` is respected). Wide terminals (≥ 120 columns) get three columns; from 80×24 up you
still get all five panes in a tighter arrangement. Smaller than that, the desk says so and
accepts no key but `q` — nothing can be acted on that is not on screen.

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

One daemon only, and only over a local unix socket. No context switching, no Swarm, no compose
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

## How it was built, and why

| | |
| --- | --- |
| [`docs/DECISIONS.md`](docs/DECISIONS.md) | every design decision, the alternatives, and the reason — including the ones forced by reality during the build |
| [`docs/JOURNAL.md`](docs/JOURNAL.md) | the build in order: what was made, checked, found and changed |
| [`docs/TESTING.md`](docs/TESTING.md) | how it is proven without touching real containers, the measurements, and what is *not* proven |
| [`openspec/changes/docker-desk-workspace/`](openspec/changes/docker-desk-workspace/) | the spec it was built to: proposal, eleven capability specs, design, tasks |

One thing from TESTING.md belongs here too: by design, **no mutating request from this program
had ever been sent to a real Docker daemon when it was handed over** — the build was required
not to touch real containers. Its requests are encoded by Docker's own client library and were
exercised against a fake engine that Docker's own CLI was used to validate. The first `stop`
you confirm on a real container will be the first one ever. `make sandbox` and `--read-only`
exist so that you can decide when that is.

## Layout of the code

```
cmd/docker-desk        the program: resolve the daemon, check it, then open the desk
cmd/fake-dockerd       the sandbox engine
internal/dockerctx     which daemon the current Docker context means; why it cannot be reached
internal/engine        the only door to the daemon (Docker's client library behind a narrow interface)
internal/plan          what a mutation will do, decided by pure code before anything is sent
internal/jobs          background jobs and their ledger
internal/live          bounded buffers and stream supervision for logs, stats and events
internal/shell         the interactive exec session and the terminal hand-off
internal/ui            the desk: panes, keys, dialogs
internal/fakeengine    an in-memory Docker Engine speaking the real HTTP API (tests, sandbox)
internal/ptyscreen     runs a program on a pseudo-terminal with an emulated screen (tests)
internal/e2e           the real binary, driven like a person would
scripts/               cross-check of the fake engine with the real docker CLI
```

```sh
make test         # everything, against the fake engine only
make race         # the same under the race detector
make crosscheck   # the real docker CLI against the fake engine
make help         # the rest
```
