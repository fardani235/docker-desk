# Design

## Context

See `proposal.md` for the motivation and `specs/container-attention/spec.md` for the contract.

- The desk already has the full-screen-view pattern: `internal/ui/space.go` opens `S`, saves the
  pane/kind/highlight it interrupted, runs its reads as cancellable work behind a version
  counter the frame tick consumes, renders rows with provenance, and restores the desk on close.
  Files (`F`) and Repository (`B`) follow the same shape; keys are dispatched in
  `internal/ui/keys.go`.
- `engine.Container` already carries `State`, `Status` and `Health`; `mobyEngine.Containers`
  fills them from one `GET /containers/json?all=1`. Health does not need an inspect.
- `engine.ContainerDetail` adds `ExitCode`, `OOMKilled`, `RestartCount`, `FinishedAt` and
  `Error`, from one inspect per container.
- `internal/session` holds every member's engine, endpoint, link, hub and state. Only the
  focused member's lists are refreshed today; unfocused members keep an event stream and a ping
  (`specs/daemon-session/spec.md`). `Session.TakeNotices` already scans background event rings
  for `die` and `oom`.
- `internal/live` keeps each member's bounded event ring; `internal/ui/eventnav.go` already has
  the wait-for-an-object-in-a-list pattern used by Events-pane jumps (`JumpWait`).
- The Containers list draws `stateGlyph(state) + " " + state` in its STATE cell; health appears
  only inside the daemon's STATUS prose today.

## Goals / Non-Goals

**Goals**

- One screen, one key, one answer across every connected daemon, with the focused daemon's block
  instant and the others filled with per-block provenance.
- A classification the operator can argue with: explicit rules, explicit “unknown”, no
  container called fine without evidence.
- No polling. Reads happen on open, on `r`, and when a daemon's own events say something
  changed.
- Enter lands on the object in the desk; Esc puts the operator back exactly where they were.

**Non-Goals**

- Acting from the view (no start, stop, remove, shell, logs inside it). The desk's plans and
  confirmations stay the only path to a mutation.
- Images with moved upstream tags, disk pressure, compose drift, registry state, or any second
  kind of object.
- A permanent dashboard, a notification system, or persisted history. Closing the view ends its
  reads and its memory.
- Changing the default order of the Containers list.

## Decisions

### D1 — A full-screen view, not a scope on the Containers list

`H` opens a view shaped like Space, not a filter mode. The operator's question is
session-wide, and a list scope would have to define what “select all”, marks and actions mean
while rows are hidden or reordered. The view leaves the list exactly as the CLI presents it.

*Alternatives rejected:* a scope toggle in the list (cheaper, but per-daemon and it complicates
selection/filter semantics); pinned problem rows at the top of the list (alters a faithful list
for every operator).

### D2 — Session-wide, with the focused daemon first

The view shows one block per connected member: the focused one first, then the session's order.
A disconnected member is a block with its endpoint and reason, no rows. This is the one feature
that uses the desk's multi-daemon premise for triage, and it is what justifies a separate screen
over a list scope.

*Why the focused block is instant:* `m.snap.Containers` is already loaded for it, so the view
draws immediately; the other members are read in parallel and fill in.

*Alternatives rejected:* focused-daemon-only (then the list scope is the better shape);
a timer-refreshed dashboard for every member (violates the daemon-session rule and costs a
request per daemon per tick).

### D3 — State and health from the list; details only for candidates

The first pass is one `Containers()` per member. It yields state, health, image, ports and the
daemon's status text, which is enough for the “needs you” signals restarting, unhealthy and
dead. Exit code, OOM, finish time and restart count come from `InspectContainer`, and only for
containers whose class needs them: exited/restarting/dead containers, plus a second pass over
running containers for the flapping check.

The daemon's STATUS prose is never parsed for an exit code or an OOM verdict: it is not a
contract, and the desk already refuses to read daemon prose as data.

*Alternatives rejected:* inspecting every container on every read (N requests for information
most rows do not need); parsing `Exited (137)` out of the status line (cheap but dishonest);
dropping flapping from v1 (it is the signal that catches a container that is up at the moment
but keeps dying).

### D4 — A pure classification package

A new `internal/attention` package holds the ladder, ordering and counts. It takes the list
snapshot plus whatever details have been read and returns rows and counts; it has no engine, no
UI and no clock beyond a `now` argument.

Precedence, so each container has one class:

1. state `dead` → needs you;
2. state `restarting` → needs you;
3. state `exited` with a known non-zero code → needs you (OOM named); with code 0 → not a
   problem; with no code yet → reading;
4. state `paused` or `removing` → not a problem;
5. state `created` → watch;
6. state `running` and health `unhealthy` → needs you;
7. state `running` and restart count ≥ 3 → watch;
8. state `running` and health `healthy` → fine;
9. state `running` with no healthcheck or health `starting` → unknown.

The order of needs-you rows is restarting, unhealthy, dead, then exited by finish time
(newest first); watch rows follow. The classes fine, unknown and not-a-problem are counts.

*Why a separate package:* the ladder is the part most likely to be argued about, and it should
be testable without a terminal or a fake daemon.

*Alternatives rejected:* logic inside the UI (hard to test, easy to drift from the spec);
one eager classification that calls the engine (mixes I/O with rules).

### D5 — One view-owned run, not a job per daemon

Opening the view starts a cancellable `attentionRun` (the Space pattern): one goroutine per
member reads its container list and then its candidate details, publishing per-member results
under a mutex with a version counter. The frame tick copies what is new and redraws. Closing the
view cancels the run; a member removed from the session cancels its read.

Jobs are not used because a read-only glance should not fill the Jobs ledger with one job per
daemon, and because the view's lifetime, cancellation and partial results are its own concern.
The read-only mode needs no special case: every request is a `GET`.

*Alternatives rejected:* `jobs.StartOn` per member (ledger noise, lifetime coupling); a Bubble
Tea message per result (breaks the desk's rule that live data is not message traffic).

### D6 — Refresh on open, on `r`, and on events — never on a timer

Each block records its read time. While the view is open, the frame tick looks at each member's
`live.Hub` event ring; new `die`, `oom` or `health_status` entries mark that block stale and
start a fresh read, debounced (at most one read per member per second). No periodic request is
made. A failed read keeps the previous rows, labelled with their age, and adds the failure — it
never turns old rows into current ones.

*Alternatives rejected:* a 5-second tick for every member (a background refresh by another
name); manual `r` only (a dashboard that silently ages is worse than no dashboard).

### D7 — Jump by focusing, waiting, then selecting; restore on failure

A row carries its member key and container ID. Enter closes the view, focuses that member,
requests its lists and waits up to `JumpWait` for the container to appear (the Events-pane jump
already does this), then selects it. If the member is disconnected or the container does not
appear, the desk says which and focuses the member that was focused before. Esc or `H` restores
the exact pane, kind and highlight saved when the view opened.

*Alternatives rejected:* switching focus first and leaving the operator there on failure (loses
their place); jumping by ID with no wait (the list may still be loading).

### D8 — The ambient cue counts only certain, cheap signals

The Containers STATE cell gains the health word when the daemon reports one (`running ·
unhealthy`, warning tone); a health word the daemon did not report is never invented. A count of dead +
restarting + unhealthy containers appears in the Kinds entry and the Containers title, labelled
so it cannot be mistaken for the view's total: “3 need attention — H reads exit codes and
restarts too”.

Computing the view's full count for the ambient cue would mean inspecting every exited and
running container on every list refresh, which is exactly the cost the view avoids. The cue is
honest about being a subset; the view is the full answer.

*Alternatives rejected:* full count in the header (requires background inspects); no cue at all
(the view is a key the operator must remember).

### D9 — Ordering, counts and the empty state

Within a block: needs-you before watch, with the class order above; newest failure first among
exited; then a counts line for fine / unknown / not-a-problem / still-reading. The block header
shows the daemon name, endpoint, context/link tag, engine version, container total and read
time. When nothing needs a person, the block says so with its totals; when the whole session is
clear, the view says that once, not once per daemon.

*Alternatives rejected:* one global list across daemons with a daemon column (harder to scan and
to jump from); grouping by compose project first (the project is shown per row; service
grouping can come later if operators ask for it).

### D10 — Name it Attention; `H` opens it

“Health” in Docker means a healthcheck. The view includes restart loops, dead containers and
failed exits, and calls a container without a healthcheck *unknown*, not healthy. “Attention”
describes the ranking without implying a medical verdict. `H` is free in the global key map and
is mnemonic enough; `Esc` and `H` both close.

*Alternatives rejected:* “Health” (wrong claim), “Problems” (implies only bad states, though the
view also watches), `!` (needs Shift and reads as negation).

### D11 — Responsive, mouse-aware, read-only

The view uses the existing overlay conventions: it owns the screen, a title with counts and the
read time, a scrollable body, a hints line, `?` for help, `q` for quit, and mouse rows/wheel
through the `hit` machinery. At 80×24 one block is visible at a time; blocks and rows scroll.
No key in the view sends a mutation; actions are reached by Enter on the desk.

## Risks / Trade-offs

- **Inspect fan-out on a busy or remote daemon.** The flapping pass inspects running containers,
  and the failure pass inspects exited ones. → Bound the pass, fill progressively, allow cancel,
  and state how many rows were not inspected. The first answer (state and health) never waits
  on it.
- **Cross-daemon reads strain the session rule.** → The spec exception is limited to an
  explicitly open view; reads stop on close; no timer; event-driven re-reads only.
- **A dashboard is trusted as live.** → Every block shows its read time; a failed or old read
  is labelled and keeps its age; no wording suggests a live subscription.
- **Jump races with a changing list.** → Focus, wait with `JumpWait`, select; on failure focus
  back and say why.
- **Old failed containers make the list noisy.** → Newest first, counts, and the operator can
  remove them on the desk. An age filter is a later, separate decision; hiding failures would
  make the screen lie.
- **Flapping threshold.** Three restarts is a judgement call; it is one constant in the pure
  package, easy to tune once operators use it.
- **No healthcheck means unknown.** Operators may read a wall of “unknown” as noise. → Counts,
  not rows, and no claim of health either way.
- **A daemon removed while the view is open.** → Its block disappears, its read is cancelled,
  the rest stays usable.

## Migration Plan

Additive and self-contained: a new key, a new view, a state-cell change and a count. No daemon
state, no configuration, no flag and no stored data. Rollback is reverting the code; nothing on
any daemon changes. Documentation to update with the implementation: README's key tables and a
new Attention section, the in-app help, and the project's decision/testing records.
