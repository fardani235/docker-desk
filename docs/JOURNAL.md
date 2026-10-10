# Build journal

Chronological record of what was built, what was checked, what was found and what changed
because of it. Decisions and their alternatives live in [DECISIONS.md](DECISIONS.md); this file
is the story of how they met reality. Newest entries are at the bottom.

Times are omitted on purpose: the order is what matters.

---

## 0. Before writing anything

**Looked at the machine.** Go 1.26, Node 24, Python 3.12 present; no Rust. `~/go/bin` is on
`PATH`. Docker Engine 29.8.2 (API 1.56) on `unix:///var/run/docker.sock`, context `default`,
containerd image store.

**Looked at what must not be touched.** The daemon holds a running container
(`optimistic_boyd`), an exited one (`minikube`) with a volume and a network of the same name,
and three images. Recorded a read-only baseline of every container (ID, state, started/finished
times, restart count), image, volume and network so that it can be diffed at the end
(see [TESTING.md](TESTING.md)).

**Two constraints discovered that shaped the plan.**

- Terminal multiplexers are not permitted in the build session and there is no interactive
  terminal. End-to-end proof therefore has to come from a scripted pseudo-terminal with a
  terminal emulator, driven from Go.
- Compound shell commands are sometimes refused by the session's permission rules, so checks
  are kept as small, single-purpose commands or as scripts in the repository.

**Process note.** The repository's OpenSpec "propose" workflow says to stop after writing the
planning artifacts and wait for a go-ahead. The request for this work said the opposite in
advance ("writing the spec is not a place to stop"). The explicit request wins; the build
continued straight after the spec validated.

## 1. Spec

Wrote `openspec/changes/docker-desk-workspace/`: proposal, eleven capability specs, design
(23 decisions), 37 tasks, each task carrying its own verification. `openspec validate --strict`
passes.

The decisions that change the shape of the thing, made at spec time and defended in the design:
official Moby client rather than the CLI or hand-rolled HTTP (D3); a fake *daemon* speaking the
real HTTP API rather than a mocked interface (D7); live data never travels through the UI
message queue (D8); a mutation is a plan built by pure code, shown, then executed verbatim
(D11); prune deletes an exact previewed set and never calls Docker's prune endpoints (D14).

## 2. Endpoint resolution

`internal/dockerctx`: `DOCKER_HOST` → `DOCKER_CONTEXT` → `currentContext` → default, plus the
unix-only rule and a classifier that turns connection failures into plain sentences. Tests use
temporary config directories and real unix sockets (missing, listening, closed, chmod 000).

## 3. Engine layer and the first contact with the real daemon

`internal/engine`: the `Engine` interface, the desk's own value types, the Moby-client
implementation, the read-only wrapper and the read-only HTTP request hook.

Ran the opt-in read-only test against the real daemon (`make real-readonly-check`). It lists
and inspects everything, tails logs, takes two stats samples and opens the event stream; the
engine is constructed read-only, and the test first proves that both layers of read-only
refuse a `DELETE` aimed at a name that cannot exist.

**Findings from real data, each of which changed the code:**

1. *An image can have no tag and still not be dangling.* With the containerd image store an
   image pulled by digest reports `RepoTags: ["kicbase/stable@sha256:…"]`, and the daemon's own
   `dangling=true` filter does not include it. The first version inferred "dangling" from
   `RepoTags`. Changed: the engine now asks the daemon (`dangling=true` list) and carries the
   verdict on each image; the list shows such a reference as repository + `<none>`.
2. *A stopped container still names its network by ID.* The exited `minikube` container lists
   network `minikube` with its `NetworkID` but no address, and `network inspect minikube` shows
   zero endpoints. That confirms both halves of D14: `docker network prune` would delete this
   network, and the conservative "referenced by any container" rule can be computed from the
   container list alone.
3. The daemon's per-image `Containers` count is populated in API 1.56 and agrees with matching
   containers by `ImageID`, which is what the planner uses.

## 4. The fake daemon, and what the real CLI taught it

`internal/fakeengine` (+ `cmd/fake-dockerd`): in-memory engine serving the real HTTP API on a
unix socket — lists, inspect, lifecycle, in-use refusals, events, multiplexed log framing with
follow and a configurable flood rate, stats, pull progress, exec with connection hijack and a
tiny scripted shell, plus a request log and fault injection for tests.

`scripts/cli-crosscheck.sh` runs the **real `docker` CLI** against the fake and asserts on the
results (61 checks: `ps`, `inspect`, `stop/start/pause/kill/rm`, `events`, `logs -f`, `stats`,
`exec`, `exec -it` under a pty, `pull`, `rmi`, volumes, networks). Every invocation carries an
explicit `-H` to a socket in a private temp directory and the script aborts before any mutation
unless the server identifies itself as the fake.

**Finding:** the first run failed four checks. Two were wrong expectations about CLI wording.
The other two were a real protocol misunderstanding in the fake: it answered an exec of a
missing executable with HTTP 400. The real daemon upgrades the connection first, writes the
error *into the stream*, closes it, and reports exit code 127 through exec inspect — which is
why the CLI said "unable to upgrade to tcp, received 400" against the fake. Fixed the fake, and
this is now a requirement on the shell session: a shell that cannot start is detected from the
exit code and the captured output, not from an HTTP error.

## 5. Live-data primitives, jobs, planner

`internal/live`: the ring buffer with stable absolute indices, the line writer (2 KiB bound,
never splits a UTF-8 sequence, lone `\r` rewinds the line for progress bars), the sanitiser,
the statistics maths, and the hub that supervises the event stream (reconnect with replay and
de-duplication), one log stream, and capped per-container statistics streams.
`BenchmarkLineWriter`: about 3 million lines per second on this machine.

`internal/jobs`: the job manager — bounded ledger, per-job output, cancellation, a panic in a
job becomes a failed job.

`internal/plan`: every rule about what a mutation may do, as pure functions over a snapshot,
plus `Execute`, the only place where a plan turns into requests. Tested against the fake for
the properties that matter: an object that became eligible after the preview is untouched; an
object that became in-use after the preview is refused by the daemon, recorded, and the rest
continue; project removal sends only container deletions; cancellation stops further steps.

A wrong expectation corrected rather than "fixed": I had assumed `UPPER/case` is an invalid
image reference. It is not — Docker's own parser reads an upper-case first component as a
registry host. The test now uses a name Docker really rejects.

## 6. The shell session

`internal/shell`: exec create → hijacked attach → raw mode → copy both ways → restore. Tested
on a real pseudo-terminal against the fake: terminal modes compared field by field before and
after; resize forwarded; Ctrl-C delivered as a byte; container dying mid-session; daemon
vanishing mid-session; shell that cannot start; and that a key typed after the session is still
there for the next reader (the keyboard reader is cancellable — a blocked `Read` left behind
would steal the first key pressed back at the desk).

## 7. The desk

`internal/ui`. First run on a pseudo-terminal drew all five panes with live data on the first
try, which mostly proves the layers underneath were sound. What the first real screens showed
that no unit test had:

- **The key hints did not fit.** Eight container actions plus navigation overflowed even at
  140 columns. Shortened the separators and labels; the full list is behind `?` and `Enter`.
- **Anonymous volumes hid their own marker.** A 64-character name pushed "(anonymous)" off the
  row. They are now shown as twelve characters, an ellipsis and the marker.
- **The exit code was the part that got cut off.** An event line read
  `container die shop-web-1 exi…`. Redesigned the line so the outcome follows the action
  (DECISIONS B5).
- **The log stream never opened when switching to the Logs tab** on an already-highlighted
  container. Stream synchronisation ran only when the *highlight* changed. It now runs on every
  frame once the highlight has settled (it is a no-op unless the wanted target changed). Found
  by the end-to-end session, which got stuck at "opening log stream…".
- **Dialog prose broke words in the middle** ("kills i / t."). Prose is now word-wrapped; log
  lines and JSON are still hard-wrapped so nothing is lost.
- **A typed confirmation looked pre-filled** because of its placeholder (DECISIONS B10).
- **A torn border** (`┘┘`) appeared in one captured screen and not the next. Chasing it found
  nothing wrong in the program: the raw output was correct. The harness was handing the
  terminal emulator chunks that ended in the middle of a three-byte box-drawing character.
  Fixed in the harness; kept the frame-geometry test it prompted, because a real tear would be
  just as easy to introduce.
- That same investigation turned up three characters (`▶ ⏸ ⚠`) that some terminals draw two
  cells wide. Replaced, and a test now scans the UI's source for any such character
  (DECISIONS B4).
- **Every end-to-end test took five seconds.** Bubble Tea was waiting for the terminal to
  answer a background-colour query that the emulator never answered (DECISIONS B11).

The safety tests were written as a table over every mutating action, with the fake daemon's
request log as the witness, and then checked for teeth by breaking the rules on purpose: with
Enter accepted as consent, and with a prefix of the confirmation word accepted, the suite
fails with "1 mutating request(s) reached the daemon". Both changes were reverted.

## 8. Responsiveness, measured

Model level, production frame interval, one container flooding its log with nine statistics
streams and the event stream running: about 300,000 lines per second ingested; 29 frames and
29 queue messages in 1.5 s; a key takes 0.7 ms (median) to 2–4 ms (worst) to take effect and
redraw. Real binary through a real pty at about 199,000 lines per second written, 196,000
ingested: a key shows on screen in 12 ms (median), 26 ms (p95).

Under the race detector, with four test binaries competing for four cores, ingestion fell to a
few thousand lines per second and the test failed its own precondition. That is the detector's
cost on a byte-level loop, not a property of the desk: the throughput precondition is asserted
without the detector and logged with it; every bound is asserted in both.

## 9. The real daemon, read-only

Ran the real binary with `--read-only` against the real engine through the pty harness: every
kind, every tab, both stream panes, help, and every mutating key pressed on the real objects —
each answered "Read-only mode… Nothing was sent to the daemon". It showed the two real
containers (live CPU and memory for the running one), three images, one volume, four networks,
and the `minikube` network with one container although that container is stopped.

Then compared the daemon with the baseline from §0: containers, container state (status,
started-at, finished-at, restart count), images, volumes and networks are all identical, and
the running container has no exec sessions.

## 10. Hardening the gaps between asking and answering

Reading the plan/dialog code once more with fresh eyes, before the independent review came
back: answers from the daemon arrive asynchronously, and three sequences were only safe by
accident (a busy flag happened to block the bad path):

- cancelling a form while its name check was in flight, then the check coming back;
- pressing Tab in a prune preview (recompute for the other scope) and typing the word before
  the new preview arrived;
- pressing an action key and then opening another overlay before the confirmation arrived.

Made them safe on purpose: every plan request carries a sequence number and only the answer to
the latest request is honoured; closing a dialog abandons its pending request; a prune preview
that is being recomputed confirms nothing until the new one is on screen; a confirmation never
replaces another dialog. `TestAnswersToAbandonedRequestsDoNothing` drives each sequence.

## 11. The independent review

Handed the mutation path to a separate reviewer (DECISIONS B30). It read the code, ran nothing
against any daemon, and came back with twelve findings and a list of what it had examined and
found sound.

The two that mattered most were both about *time*, which none of my tests had modelled:

- Planning reads the daemon first, so a confirmation opens a moment after its key. Press `t`,
  start typing `/mysql`, and the dialog could open in time for the `y` of "mysql" to stop a
  container. My own hardening in §10 had covered an answer arriving for a *closed* dialog, and
  a dialog arriving over *another dialog* — but not over the filter, which is not a dialog.
- Below 80×24 the desk drew only a message, yet every key was still routed. With the guard
  removed, the new test shows a real `POST …/stop` arriving at the fake while the screen says
  "terminal too small".

Fixed those and six more (menu target, name pinning, the in-use untag in prune-all, cancel
accounting, shell escape hatches, terminal-mode reset); kept three as they were, with wording
changes, and wrote down why. Added the corresponding scenarios to the specs so that the
contract says what the code now does. Every fix has a test that replays the reviewer's
sequence; for the three most serious, the test was confirmed to fail without the fix.

Changed my mind on one design-time decision because of this: a quarter-second arming delay on
confirmations, which I had rejected for making tests time-dependent, is now in (DECISIONS B25).

One flaky test surfaced while re-running everything: `make sandbox` returned before the fake
daemon it had started was fully gone. The recipe now waits for it.

## 12. Final state

`go vet`, `go test ./...` and `go test -race ./...` are clean; the cross-check with the real
`docker` CLI passes; the end-to-end suite was run six times in a row without a failure; the
real daemon was compared with the baseline once more at the very end (see TESTING.md). Nothing
was committed and nothing was installed outside the repository (DECISIONS B12, B13).
