# How Docker Desk is proven — without touching your containers

The constraint was: prove that a tool which stops, kills and deletes things works, **without
ever mutating the containers that actually exist on this machine**. This file says how that
was done, what was measured, how to repeat it, and — just as important — what is *not* proven.

## The short version

| What | With | Real daemon involved? |
| --- | --- | --- |
| Every rule about what may be changed, and how | pure planner tests | no |
| Every request the desk can send | Docker's own client library against a fake daemon | no |
| The fake daemon itself | the real `docker` CLI driving it, 61 assertions | no |
| Every key, pane and dialog | the UI model driven key by key; the fake's request log is the witness | no |
| Responsiveness under a log flood | model-level and real-binary measurements | no |
| The real program, end to end, including the shell hand-off | the binary on a pseudo-terminal with a terminal emulator | no |
| That it reads a real engine correctly | the engine layer, then the whole program, in read-only mode | **yes — `GET`/`HEAD` only, enforced** |
| That nothing on the real engine changed | before/after comparison of every container, image, volume and network | read-only |

`go test ./...` runs 194 tests and subtests (two of them opt-in and skipped by default). They pass with and
without the race detector.

## Why a fake daemon, and why it can be trusted

`internal/fakeengine` is an in-memory Docker Engine that serves the real Engine HTTP API on a
unix socket. The desk talks to it through the same client library, the same code path, as it
talks to a real daemon — so request encoding, response decoding, the multiplexed log framing
and the hijacked exec connection are all exercised for real.

A fake written alongside the code it tests can agree with that code and still be wrong. It is
therefore checked from both directions:

1. **Docker's client against the fake.** `make crosscheck` runs the real `docker` CLI against
   the fake and asserts on the results: `ps`, `inspect`, `stop`/`start`/`pause`/`kill`/`rm`,
   `events`, `logs -f`, `stats`, `exec`, `exec -it` on a pty, `pull`, `rmi`, volumes,
   networks — including the refusals ("container is running", "volume is in use", "has active
   endpoints", "pre-defined network"). 61 assertions.

   The script cannot reach the real daemon: every `docker` invocation carries an explicit `-H`
   to a socket in a private temporary directory, `DOCKER_HOST`/`DOCKER_CONTEXT` are unset
   first, and it aborts before any mutation unless the server on that socket identifies itself
   as the fake.

   The first run of this script found a real protocol error in the fake (how a process that
   cannot start is reported — see the journal, §4). That is the point of having it.

2. **The desk against Docker's server**, read-only (below).

And the request *encoding* is not ours at all: it is the Moby client's, the one the CLI uses.

## Nothing in the test suite can mutate a real daemon

This is enforced, not promised:

- Every test package that builds an engine calls `engine.InstallTestGuard()` in `TestMain`.
  After that, `engine.New` refuses a read-write connection to any socket outside the system
  temporary directory (`TestGuardRefusesRealSockets`), and `DOCKER_HOST` is pointed at a path
  that cannot exist.
- The two tests that do visit a real daemon are opt-in (`DOCKER_DESK_REAL_HOST=…`) and open it
  with `ReadOnly: true`. In read-only mode a hook on the HTTP client rejects every request
  that is not `GET` or `HEAD` before it is written to the socket. The test proves the hook
  first, by aiming a `DELETE` at a name that cannot exist.
- The end-to-end harness starts the binary with a minimal environment; your `DOCKER_*`
  variables are never inherited.

## The safety properties, as tests

All in `internal/ui/safety_test.go` unless noted. The witness in each case is the fake
daemon's request log — what actually arrived — not the UI's opinion of what it did.

- **`TestNoSingleKeystrokeMutates`** — for each of the 18 confirm-tier and typed-tier actions
  (start, stop, restart, pause, unpause, kill, remove container, remove image, untag image,
  remove volume, remove network, connect, disconnect, stop project, remove project, prune
  images/volumes/networks):
  1. the action key alone sends nothing and opens a dialog showing the title, the object, the
     effect and the daemon endpoint;
  2. Esc sends nothing and leaves no job;
  3. for `y` confirmations, Enter, space, `Y`, other letters and Tab send nothing; for typed
     confirmations, Enter on empty input and a mistyped word send nothing and the dialog says
     so;
  4. the deliberate input sends exactly the announced request(s), none carrying `force`.
- **`TestEveryActionKeyIsCovered`** — an action added to the desk without a row in that table,
  or not marked as mutating, fails the build's tests.
- **`TestBlockedDeletionsCannotBeConfirmed`** — ten in-use cases (running and paused
  containers; images used by running and by stopped containers; volumes; networks, including
  one referenced only by stopped containers; a predefined network; a running compose project):
  the dialog says what uses the object, offers no input, and no key proceeds.
- **`TestConfirmationIsBoundToTheObjectItShowed`** — the list is reshuffled under an open
  confirmation and the highlight is even moved elsewhere; confirming acts on the object the
  dialog named.
- **`TestPruneShowsTheExactSetAndDestroysOnlyThat`** and, at the planner level,
  **`TestPruneDeletesExactlyTheListedSet`** — a volume created after the preview survives the
  prune; exactly one `DELETE` per listed object is sent; nothing resembling a prune endpoint.
- **`TestListedObjectThatBecameInUseIsRefusedAndTheRestContinue`** (`internal/plan`) — the
  daemon's refusal is recorded for that object, it is not deleted, the others are.
- **`TestNetworkPruneKeepsNetworksOfStoppedContainers`** — the conservative in-use rule.
- **`TestProjectRemoveTouchesOnlyItsContainers`** — only container `DELETE`s; networks and
  volumes counted before and after.
- **`TestReadOnlyDisablesEveryAction`**, **`TestReadOnlySendsNothing`**,
  **`TestReadOnlyCoversEveryMutation`** (`internal/engine`, by reflection over the interface),
  and **`TestReadOnlyFlag`** (the real binary: every request it sent was `GET`/`HEAD`).
- **`TestNeverForces`** (`internal/engine`) — calls every mutating method and reads the
  queries that went over the wire.

- **`TestAnswersToAbandonedRequestsDoNothing`** — the gaps between asking the daemon and
  getting the answer: a form cancelled while its check was in flight is not submitted; a prune
  preview being recomputed cannot be confirmed; a late confirmation does not replace another
  overlay; and keys typed for something else — a filter word containing `y`, Enter in a
  picker, `t` followed at once by `y` — cannot confirm a dialog that opens late.
- **`TestConfirmationsMustBeReadFirst`** — within the arming delay `y`, the typed word and
  Enter on the pre-filled shell form are ignored (and early text is not kept); afterwards they
  work; Esc is never delayed.
- **`TestNothingActsWhileTheDeskCannotBeDrawn`** — in a too-small terminal sixteen keys send
  no request at all and change no state; a dialog open when the window shrinks cannot be
  confirmed and is back, unconfirmed, when it grows.
- **`TestActionMenuActsOnTheObjectItNames`** — the highlight is moved, and the object removed,
  under an open menu.
- In `internal/plan`: **`TestReferenceMovedAfterConfirmationIsRefused`**,
  **`TestUntagThatWouldNowDeleteIsRefused`**,
  **`TestPruneDoesNotUntagAnImageThatBecameInUse`**,
  **`TestVolumeRecreatedAfterConfirmationIsRefused`** — the name-pinning checks, each asserting
  that zero mutating requests were sent.

**Do these tests have teeth?** They were checked by breaking the code on purpose. Three of the
review fixes were taken out one at a time: without "a key drops the pending action" the filter
test fails with "the confirmation opened under the filter"; without the too-small guard the
fake's log shows a real `POST …/stop`; with the menu re-reading the highlight the plan targets
the wrong container. And for the original rules: Making Enter
count as consent fails `TestNoSingleKeystrokeMutates/stop` with "1 mutating request(s) reached
the daemon"; accepting a prefix of the confirmation word fails `…/remove_volume` the same way.
(Both changes were reverted.)

## The independent review

After the suite was green, the code between a key press and a mutating request was given to a
separate reviewer, with the rules it must obey and nothing about why it was believed to obey
them, and asked for concrete sequences that break a rule. It found eight things worth fixing —
the most serious being a confirmation that could be confirmed by keys typed for something
else, and dialogs that stayed live in a terminal too small to draw them. All are fixed and
have tests; the list, with what was kept as it was and why, is in
[DECISIONS.md](DECISIONS.md#b30--an-independent-review-of-the-mutation-path-before-calling-it-done).

It also recorded what it examined and found sound: Enter never confirms a `y` dialog; the
typed tier needs an exact match; blockers are enforced three times over; no force option is
set anywhere; no prune endpoint is called; project removal emits only container removals;
read-only mode covers every mutation and exec.

The review read code; it ran nothing against any daemon.

## Responsiveness: measurements

All figures are from this machine (4 cores, Linux), with the fake daemon in the same process
or on the same host. They are evidence that the bounds hold by design, not benchmarks to
compare across machines.

**Model level** — `TestFloodDoesNotStarveTheDesk` (`internal/ui/responsive_test.go`). One
container floods its log while nine statistics streams and the event stream run, at the
production frame interval of 50 ms.

| Measured | Result | Bound asserted |
| --- | --- | --- |
| Log lines reaching the desk | ≈ 300,000 per second | ≥ 50,000 for the test to mean anything |
| Frames rendered in 1.5 s | 29 | ≤ 1.5 s ÷ 50 ms + 3 — the frame rate, not the line rate |
| Messages through the UI queue in 1.5 s | 29 | ≤ frame ticks + 30 — i.e. no stream data in the queue |
| Key → new state + full redraw | p50 0.7 ms, p95 1.5 ms, max 2–4 ms | < 100 ms |
| Log buffer after the flood | exactly 5,000 lines | = 5,000 |
| Statistics history per container | ≤ 120 samples | ≤ 120 |

Related: `BenchmarkFrame` — one full redraw at 180×50 while ingesting 200,000 lines/s costs
about 0.66 ms. `BenchmarkLineWriter` — splitting and sanitising log output runs at about
3 million lines per second. `TestEventBurstCausesBoundedRefreshes` — 101 events cause one or
two list requests. `TestIdleDeskDoesNotRedraw` — with nothing streaming, 50+ frame ticks render
zero frames. `TestLongLinesAreBounded` — a 3 MiB line with no newline is split into 2 KiB
pieces.

**Real binary through a real pseudo-terminal** — `TestResponsiveUnderFlood`
(`internal/e2e`). The program runs as a separate process; Tab is pressed 40 times and the time
until the emulated screen shows the focus on the next pane is measured.

| Measured | Result | Bound asserted |
| --- | --- | --- |
| Lines written by the container | ≈ 199,000 per second | — |
| Lines ingested by the desk (from its own counter) | ≈ 196,000 per second | ≥ 50,000 |
| Key → visible on screen | p50 ≈ 12 ms, p95 ≈ 26 ms, max ≈ 27 ms | p95 < 100 ms |

Under the race detector these two timing figures are logged rather than asserted. In the
model-level test, stream ingestion itself runs an order of magnitude slower, so the "≥ 50,000
lines/s" precondition can fail; the frame, queue and buffer bounds are still asserted. In the
real-binary test the program is built without the detector, but the harness measuring it — the
fake daemon generating the flood, the terminal emulator, the polling loop — is slowed several
times over while competing with the other test binaries for four cores, and the latency it
reports (p95 around 100 ms in such a run) is no longer the desk's.

## End to end: the real program on a pseudo-terminal

`internal/ptyscreen` runs a program on a pty and keeps an emulated screen of what it draws;
`internal/e2e` uses it to drive the real `docker-desk` binary the way a person does. (Terminal
multiplexers were not available to the build session; this harness is what replaced them.)

`TestSession` is one continuous sitting, asserting on the screen and on the fake's request log
at each step: the five panes and the endpoint → moving through the kinds → filtering to a
container → `t` opens a confirmation and sends nothing → Enter is not consent → `y` → the
engine's events appear in the Events pane and the job in the Jobs pane → logs of the stopped
container → start it and follow → statistics → inspect → pull an image as a background job
while navigating elsewhere → prune with a preview, a scope toggle, a mistyped word and then the
real one → a refused removal → **the shell**: the terminal is handed over (the desk leaves the
alternate screen), keys reach the process, the container reports the terminal's real size, and
on `exit` the desk is back with the same filter, highlight and focus → help → quit, with the
alternate screen left.

Screens captured during that session are in [`docs/screens/`](screens/) — plain text, exactly
what the emulator showed. `make e2e` regenerates them.

Other end-to-end tests: the 80×24 layout and resizing down to "too small" and back; `NO_COLOR`
(no colour sequence in the entire output, focus still marked); `--read-only`; the daemon
stopping and restarting under a running desk; refusing to open without a daemon, and with a
remote endpoint (the alternate-screen sequence never appears in the output); quitting with a
job running; and `make sandbox` as documented.

The shell's terminal handling has its own pseudo-terminal tests (`internal/shell`): raw mode
during the session, the exact terminal modes restored afterwards (compared field by field),
resize propagation, Ctrl-C delivered to the remote process, the container dying mid-session,
the daemon going away mid-session, a shell that cannot start, a daemon that never starts the
process (bounded wait), the local escape (Ctrl-] three times), `SIGTERM` ending the session
cleanly, terminal modes switched off afterwards, and — the subtle one — that no reader is left
behind to swallow the first key typed back at the desk.

## The real daemon: looked at, never touched

Two opt-in checks talk to the real engine, both read-only:

```sh
make real-readonly-check     # the engine layer: list, inspect, logs tail, stats, events
DOCKER_DESK_REAL_HOST=unix:///var/run/docker.sock \
  go test ./internal/e2e/ -run TestRealDaemonReadOnlyTour -count=1 -v -show-screens
```

The first found three things about real data that changed the code (journal, §3). The second
runs the real binary with `--read-only` against the real daemon and visits every kind, every
Details tab, the Events and Jobs panes and the help, pressing every mutating key on real
objects along the way — each is answered with "Read-only mode… Nothing was sent to the
daemon". On this machine it showed the two real containers with live CPU and memory for the
running one, three images, one volume and four networks; the `minikube` network was shown with
one container although its only container is stopped, which is the conservative rule at work.

**Before and after.** A snapshot of the real daemon was recorded before the first line of code
was written and again after all of the above:

| Compared | Fields | Entries | Result |
| --- | --- | --- | --- |
| Containers | full ID, name, image, state, creation time | 2 | identical |
| Container state | full ID, status, started-at, finished-at, restart count | 2 | identical |
| Images | full ID, repository:tag (including intermediate) | 3 | identical |
| Volumes | name, driver | 1 | identical |
| Networks | full ID, name, driver | 4 | identical |

The running container's started-at is unchanged, its restart count is unchanged, and it has no
exec sessions. To repeat it on your own machine, record the output of these before and after
and `diff` them:

```sh
docker ps -a --no-trunc --format '{{.ID}} {{.Names}} {{.Image}} {{.State}} {{.CreatedAt}}' | sort
docker inspect --format '{{.Id}} {{.State.Status}} {{.State.StartedAt}} {{.State.FinishedAt}} {{.RestartCount}}' $(docker ps -aq) | sort
docker images -a --no-trunc --format '{{.ID}} {{.Repository}}:{{.Tag}}' | sort
docker volume ls --format '{{.Name}} {{.Driver}}' | sort
docker network ls --no-trunc --format '{{.ID}} {{.Name}} {{.Driver}}' | sort
```

## What is not proven

Stated plainly, because a control plane deserves it:

- **No mutating request from the desk has ever been sent to a real Docker daemon.** That is a
  direct consequence of the constraint. What stands in its place: the requests are encoded by
  Docker's own client library; they were tested against a fake; and the fake was validated by
  Docker's own CLI. The first real `stop` or `remove` you confirm will be the first one ever.
  If you want to watch the tool act before trusting it with anything, `make sandbox` is for
  exactly that.
- **The interactive shell has not been opened in a real container.** Exec is a `POST`, so it
  is blocked in read-only mode by design. Its protocol handling was tested against the fake,
  whose exec behaviour was checked with the real `docker exec` and `docker exec -it`.
- **Only Linux was tested**, with Docker Engine 29.8 (API 1.56). macOS exposes the same unix
  socket and should work; it was not run there. Older engines are reached through API
  negotiation, also untested.
- **Screens were checked on a terminal emulator library**, not by eye in iTerm, Kitty,
  Alacritty, tmux, and so on. The frame-geometry and glyph-width tests exist to make that as
  safe as it can be without the eye.
- **Scale beyond the test sizes** (hundreds of containers, tens of thousands of images) was
  not measured. The statistics-stream cap was tested at 40 containers.
- **Event replay after a reconnect** was tested against the fake's event history; the real
  daemon keeps a similar bounded history, but its behaviour across a daemon *restart* (history
  lost) relies on the full refresh that follows every reconnect.

## Running everything

```sh
make test              # go test ./...            (fake daemon only)
make race              # the same under the race detector
make crosscheck        # the real docker CLI against the fake daemon
make e2e               # the scripted session; rewrites docs/screens/
make real-readonly-check   # optional: GET/HEAD-only check against your real daemon
make sandbox           # try it yourself against the fake
```
