## Context

Greenfield repository. See `proposal.md` for motivation and scope and `specs/` for the
behaviour contract; this document covers how it is built and why.

Facts observed on the target machine before designing (they shaped several decisions):

- Go 1.26, Node 24, Python 3.12 and `uv` are installed; there is no Rust toolchain.
  `~/go/bin` is already on `PATH`, so `go install` yields a launchable command.
- Docker Engine 29.8 (API 1.56, minimum 1.40) on `unix:///var/run/docker.sock`, context
  `default`, containerd image store.
- The daemon holds real objects that must not be disturbed: a running container, an exited
  `minikube` container with its volume and network, three images. One of them is instructive:
  the `minikube` network is referenced only by a *stopped* container, which `docker network
  prune` would delete.
- No interactive terminal is available to the build session and terminal multiplexers are not
  permitted, so end-to-end proof has to come from a scripted pseudo-terminal.

Constraint from the request that outranks convenience: the build must be proven without
mutating the operator's containers.

## Goals / Non-Goals

**Goals:**

- One static binary, `docker-desk`, installable with `go install` / `make install`.
- A UI whose state transitions are plain functions, so that the safety rules (what a key can
  and cannot cause) are unit-testable without a terminal.
- A hard separation between *deciding* a mutation (pure planning over a snapshot) and
  *executing* it (a job), so that what the confirmation showed is literally the data the job
  executes.
- Render cost and memory bounded by constants, not by stream volume.
- Every mutating code path exercised in tests against something that speaks the real Engine
  HTTP API, with zero mutating requests ever sent to the real daemon during development.

**Non-Goals:**

- Feature parity with the Docker CLI or with lazydocker/k9s-style tools.
- Configuration files, themes, user-definable key bindings.
- Windows named pipes. (macOS and Linux both expose a unix socket; only Linux is tested here.)
- Anything listed under "Explicitly out of scope" in the proposal.

## Decisions

### D1. Go, shipped as a single binary

Go is on the machine, compiles to one self-contained executable that `go install` puts on the
operator's `PATH`, has goroutines and channels that map directly onto "many independent live
streams", and is the language of Docker's own client library.

- *Python + Textual* — excellent widgets, but needs an interpreter environment at run time
  (venv/pipx), and a GIL-bound event loop is the wrong place to absorb a 50k-lines/s log flood.
- *Rust + ratatui* — no toolchain installed, and no first-party Docker client.
- *Node + Ink* — React reconciliation per frame is a poor fit for high-rate streams.

### D2. Bubble Tea v1 + Lip Gloss, with hand-written panes

Bubble Tea's Elm-style `Update(msg) → model` / `View() → string` makes the whole UI a
deterministic state machine: tests feed key messages and assert on state and on the rendered
text. It also has a first-class "release the terminal, run something, take it back"
primitive (`tea.Exec`), which the interactive shell needs.

Panes (table, scrolling text, tabs, dialogs) are written here rather than taken from the
`bubbles` component set: each pane must render from a shared ring buffer in O(visible rows)
and must be able to report "nothing changed" cheaply (D8). Only the single-line text input is
reused from `bubbles`.

- *tview/tcell* — richer widgets, but widgets own mutable state and draw themselves from any
  goroutine via `QueueUpdateDraw`, which makes both frame coalescing and deterministic tests
  harder.
- *Bubble Tea v2* — newer API; v1 is the long-stable line and everything needed exists in it.

### D3. The official Moby client library, not the `docker` CLI and not hand-rolled HTTP

All daemon access goes through `github.com/moby/moby/client` (the current home of Docker's Go
client; its API module is at 1.56, matching the daemon here) with API-version negotiation.

The deciding argument is about what cannot be tested: mutating requests may never be sent to
the real daemon during development. If the request encoding were hand-written, a fake engine
written by the same hand would faithfully mirror any misunderstanding. Using the library the
Docker CLI itself uses means the encoding of every stop/remove/exec is Docker's, not ours.

- *Shell out to `docker ...`* — simplest, but adds a runtime dependency on a binary, forces
  parsing of human-oriented output or per-call process spawns, gives no structured errors
  (409 "in use" vs 404), and makes streaming many things at once expensive.
- *Hand-rolled HTTP over the socket* — smallest dependency tree and full control; rejected for
  the trust argument above.

Cost accepted: a larger dependency tree (OpenTelemetry comes along transitively).

### D4. Context resolution re-implemented in ~100 lines instead of importing `docker/cli`

The client library does not know about Docker contexts; the CLI does. Importing the CLI's
context store would pull in most of `docker/cli`. The rules are small and stable, so they are
implemented directly: `DOCKER_HOST` → `DOCKER_CONTEXT` → `currentContext` in
`$DOCKER_CONFIG/config.json` (default `~/.docker`) → `default`; a named context's endpoint is
read from `contexts/meta/<sha256(name)>/meta.json`. The resolver returns both the endpoint and
*where it came from*, which the header and the launch error print.

### D5. Unix sockets only; anything else is refused at launch

"No connecting over the network" is enforced rather than left to chance: `tcp://`, `ssh://`,
`npipe://` endpoints produce a plain refusal naming the endpoint. A context that points at a
remote host is a likely way to delete things on the wrong machine; refusing is cheaper than
being careful.

### D6. A narrow `engine.Engine` interface with the desk's own types

The UI, the planner and the job runner depend on one interface that returns small value types
(`Container`, `Image`, `Volume`, `Network`, `Event`, `StatsSample`…), not on client-library
types. This keeps library churn in one package, and lets read-only mode be a wrapper (D17).
Mutating methods take the immutable identifier captured when the confirmation opened.

### D7. The test double is a fake *daemon*, not a fake interface

`internal/fakeengine` is an in-memory Docker Engine that serves the real HTTP API on a unix
socket: lists, inspect, lifecycle, logs (multiplexed stream framing), stats, events, exec with
connection hijack, pull progress, 409/404 error bodies. Tests point the real client library at
it, so encoding, decoding, stream demultiplexing and hijacking are all exercised.

It is cross-checked from both directions, because a fake written alongside the code can agree
with it and still be wrong:

1. the real `docker` CLI is run against the fake (`docker -H unix://… ps/stop/rm/exec/…`), so
   the fake's behaviour is validated by Docker's own client;
2. the desk's engine layer is run against the real daemon for read-only calls, so its decoding
   is validated by Docker's own server.

The fake is also shipped as `cmd/fake-dockerd`, a sandbox the operator can point the desk at
(`make sandbox`) to try destructive actions safely.

- *Mock the `Engine` interface only* — much less code, but would leave the client-library
  integration, stream framing and the exec hijack untested.
- *Throwaway containers on the real daemon* — closest to reality, but it creates objects on a
  machine that holds things the operator cares about, needs an image pull, and one bug in
  test cleanup becomes a real deletion. Rejected: the real daemon receives no mutating request
  at all during development.
- *Docker-in-Docker* — needs a privileged container on the same daemon; same objection.

### D8. Live data never travels through the UI message queue

This is the decision that keeps the desk responsive. A per-line message would let a noisy
container flood Bubble Tea's queue ahead of key presses and trigger a render per line.

- Each stream (logs, each container's stats, events, job progress) is read by its own
  goroutine into a mutex-guarded, fixed-capacity ring buffer that drops the oldest entry, and
  bumps an atomic version counter. Nothing is sent to the UI.
- The UI has one frame tick (50 ms). On a tick it compares version counters with the ones it
  last drew; if none moved, the cached frame is reused and nothing is redrawn.
- Rendering a pane copies only the rows that are visible from the ring, so a frame costs
  O(rows on screen) whether 10 or 10 million lines arrived since the last frame.
- Key presses are ordinary messages on a queue that streams never write to, so their latency
  is one `Update` + one `View`.

Bounds: 5,000 log lines, 2 KiB per line (longer output is split), 1,000 events, 120 stats
samples per container, 50 retained jobs with 500 output lines each.

- *Message per line with batching* — still couples key latency to stream volume through the
  shared queue; batching windows are another thing to tune.
- *Back-pressure the daemon (stop reading when behind)* — a follow view wants the *latest*
  output; blocking the reader makes the daemon buffer instead and shows stale lines.

### D9. Lists refresh on events (debounced) plus a slow poll

Engine events mark the affected kinds dirty; a 150 ms debounce turns a burst into one refresh
per kind. A 5 s poll covers anything events do not report (the "Up 3 minutes" text, missed
events across a reconnect). Requests carry a sequence number so a slow, stale response cannot
overwrite a newer one.

### D10. Jobs: one goroutine each, cancellable, observed through snapshots

A job is a function receiving a context and a reporter (`Step`, `Progress`, `Log`). The
manager keeps a bounded list and a version counter; the UI reads snapshots on the frame tick
(same mechanism as D8). Every mutation, even a fast `start`, is a job: one code path, one place
errors land, and the Jobs pane becomes a ledger of everything the desk asked the daemon to do.

### D11. A mutation is a *plan* built by pure code, then shown, then executed verbatim

`internal/plan` turns (action, target, snapshot of the engine) into a `Plan` value: title,
exact target (kind, name, ID), effect sentences, the list of objects to act on, blockers, and
the confirmation tier. The dialog renders the plan; the job executes the same plan value. There
is no second lookup between showing and doing, which is what makes "confirm the exact object"
and "prune destroys only what it showed" true by construction rather than by care.

Because planning is pure, the rules are tested exhaustively without a UI or a daemon.

### D12. Confirmation tiers

| Tier | Actions | To proceed |
| --- | --- | --- |
| confirm | start, stop, restart, pause, unpause, kill, connect, disconnect, project stop | press `y` (Enter does nothing; `n`/Esc cancels) |
| typed | remove container/image/volume/network, project remove, every prune | type the word shown (`remove` / `prune`), then Enter |
| form | pull image, create volume, create network, open shell | type/accept the value, then Enter |

- *Type the object's name (GitHub style)* — strongest, but dangling images have no name and
  anonymous volumes are 64 hex characters; a control that is unbearable gets resented and
  worked around. A fixed word still cannot be produced by one stray key or by key-repeat, and
  the dialog — not the typing — is what carries the object identity.
- *`y` for everything* — `d`,`y` becomes one muscle-memory gesture for deletion.
- *Enter confirms with focus on "Cancel"* — one mis-aimed Tab away from consent.

### D13. Nothing is ever forced; "in use" is decided twice

The engine layer has no `force` parameter at all — it cannot be passed by mistake. In-use is
checked when the plan is built (a blocker makes the dialog refuse) and again by the daemon
(a non-forced delete answers 409, which is shown). `kill` is the one deliberately forceful
action, and it is an explicit operator request.

### D14. Prune deletes an exact set; the daemon's prune endpoints are not used

Docker's prune endpoints have no dry-run and decide what to delete at the moment they run. A
preview followed by `/prune` could destroy things that became eligible after the preview. So
the plan computes the candidate set, the dialog lists it, and the job deletes exactly those
IDs, one non-forced delete each. Anything that became in-use in between is refused by the
daemon and reported per object.

"Unused" is also defined more conservatively than Docker's: a volume or network referenced by
any container — *including stopped ones* — is in use. (On this machine that is the difference
between keeping and deleting the `minikube` network.)

Trade-off: N requests instead of one, and no reclaimed-space figure for volumes.

### D15. Compose = containers grouped by label; remove touches containers only

Projects are the distinct `com.docker.compose.project` label values. Stop stops the group's
running containers; remove removes the group's containers (refused while any is running — the
same no-force rule). Labelled networks and volumes are *named* in the confirmation and left in
place: a volume is data, and removing networks is what `down` does, which is explicitly not
wanted. They remain visible under their own kinds, where they can be removed deliberately.

### D16. The shell is a native exec session, handed the terminal through `tea.Exec`

The session is implemented against the API (exec create → hijacked attach → resize) and
wrapped as a `tea.ExecCommand`, so Bubble Tea leaves the alternate screen, the session puts
the terminal into raw mode, copies bytes both ways, forwards `SIGWINCH` as exec resizes, and
on *any* exit path restores the terminal state before Bubble Tea re-enters the desk.

- *Spawn `docker exec -it`* — a few lines, but adds a runtime dependency on the CLI and a
  second, independent resolution of "which daemon" that could disagree with the header.

Default command: `sh -c 'command -v bash >/dev/null 2>&1 && exec bash || exec sh'`, editable
before launch.

### D17. Read-only mode is enforced below the UI

`--read-only` wraps the engine so every mutating method returns `ErrReadOnly` without calling
the client, and additionally installs a request hook on the HTTP client that rejects any
request that is not `GET`/`HEAD`. The second layer exists so that a future code path that
forgets the wrapper still cannot mutate. This is what makes "run it against the real daemon"
safe to do during development.

### D18. Layout

```
 Docker Desk  unix:///var/run/docker.sock  ctx default  Engine 29.8.2  ● connected
┌ 1 Kinds ────┐┏ ◆ 2 Containers (7) ━━━━━━━━━━━━━━━━━━┓┌ 4 Events ──────────────────┐
│ Containers 7│┃ NAME   IMAGE   STATE   CPU  MEM  PORTS┃│ 12:01:07 container stop web│
│ Images    12│┃ …                                     ┃│ …                          │
│ Volumes    3│┗━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┛│                            │
│ Networks   4│┌ 3 Details  Info │ Logs │ Stats │ JSON ─┐│                            │
│ Compose    2││ …                                     │├ 5 Jobs ───────────────────┤
│             ││                                       ││ ⠙ pull nginx:latest   43% │
└─────────────┘└───────────────────────────────────────┘└───────────────────────────┘
 ↑↓ move  s start  t stop  …  ? help  q quit
```

Acting happens on the left and centre; the engine's answer (Events) and slow work (Jobs) sit
on the right, Jobs in the bottom corner. Below 120 columns Events and Jobs move to a strip
under the centre column so that all five panes stay visible down to 80×24. Focus is shown by a
heavy coloured border plus a `◆` title marker (the marker and border weight survive
`NO_COLOR`). The marker was `▶` until the build found that some terminals draw it two cells
wide; see docs/DECISIONS.md, B4.

### D19. Statistics for every running container, capped

A stats stream per running container feeds the CPU/MEM columns and the Stats view; capped at
32 concurrent streams (the highlighted container always included) so a host with hundreds of
containers does not open hundreds of streams. Rates (network, block I/O) are derived from
consecutive samples.

### D20. Log output is sanitised

Container output is untrusted bytes written to the operator's terminal. Everything except SGR
colour sequences is stripped (cursor movement, screen clears, OSC title/clipboard sequences),
tabs are expanded, and invalid UTF-8 is replaced.

### D21. Keys

Global: `Tab`/`Shift+Tab` and `1`–`5` focus, `[`/`]` details tab, `?` help, `r` refresh, `q`
quit. Lists and text: `↑↓`/`jk`, `PgUp`/`PgDn`, `g`/`G`, `/` filter, `Enter` action menu.
Containers: `s` start, `t` stop, `R` restart, `p` pause, `u` unpause, `K` kill, `d` remove,
`e` shell. Images: `P` pull, `d` remove, `X` prune. Volumes: `n` new, `d` remove, `X` prune.
Networks: `n` new, `d` remove, `X` prune, `c` connect, `D` disconnect. Compose: `t` stop,
`d` remove. Logs: `f` follow, `w` wrap. Jobs: `Enter` output, `c` cancel, `x` clear finished.
Destructive actions use shifted or otherwise deliberate keys where a lowercase neighbour is a
navigation key; none of them acts without its confirmation anyway.

### D22. How it is proven

1. Unit tests: context resolution, ring buffers, sanitiser, stats maths, planner rules.
2. Engine tests: the real client library against the fake daemon, every method.
3. UI tests: key sequences against the model, asserting on state, rendered frames, and on the
   fake daemon's request log (e.g. "after `d` alone, zero mutating requests").
4. Responsiveness test: a log flood from the fake daemon while measuring key-to-state latency
   and counting frames.
5. End-to-end: the real binary under a pseudo-terminal with a terminal emulator, including the
   shell hand-off and terminal restoration.
6. Real daemon, read-only: the binary runs against the actual engine with `--read-only`; a
   snapshot of every container, image, volume and network taken before the build is compared
   with one taken after.

### D23. Documentation

`README.md` (install, launch, keys, safety model), `docs/DECISIONS.md` (this section's
decisions in ADR form plus those made during the build), `docs/JOURNAL.md` (chronological log
of what was built, verified, found and changed), `docs/TESTING.md` (how the proof works and how
to repeat it).

## Risks / Trade-offs

- [The fake daemon diverges from real Docker] → cross-validated with the real CLI and with
  read-only calls to the real daemon (D7); the client library, not our code, encodes requests.
- [Mutating paths have never run against a real daemon before the operator's first use] →
  accepted as the price of the "never mutate my containers" constraint; mitigated by D3/D7 and
  by `make sandbox` for a first hands-on trial. Stated plainly in the README.
- [Exact-set prune is slower than `/prune` and reports no volume sizes] → progress is shown per
  object; correctness of the destroyed set is worth more than speed here.
- [Conservative "unused" definition prunes less than `docker … prune`] → documented; the dialog
  states the rule.
- [Typed confirmation is friction on a frequent action] → one short fixed word; reversible
  actions stay at a single `y`.
- [A stats stream per running container costs daemon CPU] → capped at 32; same cost profile as
  `docker stats`.
- [Dependency weight of the Moby client] → accepted for D3.
- [Dropped log lines under flood] → by design: the view shows the latest 5,000 lines; the pane
  shows how many lines scrolled out.
- [Terminal left raw if the shell session panics] → restoration is deferred in the session and
  Bubble Tea restores again on return; covered by the pty test.

## Migration Plan

Not applicable: first version, nothing deployed. Rollback is deleting the binary.

## Open Questions

None that change specs, approach or tasks. Deferred by choice: per-volume disk usage, log
search, configurable key bindings.
