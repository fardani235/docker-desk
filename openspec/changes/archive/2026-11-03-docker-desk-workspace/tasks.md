## 1. Project skeleton and documentation scaffolding

- [x] 1.1 Create the Go module, `cmd/docker-desk`, `internal/` layout and a `Makefile` (`build`, `install`, `test`, `sandbox`, `vet`); verify `make build` produces `bin/docker-desk` and `bin/docker-desk --version` prints a version
- [x] 1.2 Start `docs/DECISIONS.md` (seeded from design.md) and `docs/JOURNAL.md`; verify both exist and the journal has an entry for the baseline snapshot of the real daemon

## 2. Daemon endpoint resolution and launch checks

- [x] 2.1 Implement context resolution (`DOCKER_HOST` → `DOCKER_CONTEXT` → config `currentContext` → default) returning endpoint, context name and source; verify with table-driven tests using temporary config directories, including a missing context
- [x] 2.2 Refuse non-unix endpoints and classify unreachable sockets (missing, permission denied, refused, timeout); verify with tests for each class and that the message names the endpoint
- [x] 2.3 Wire the launch sequence in `main`: resolve → refuse remote → ping with timeout → only then start the UI; verify by running the binary with `DOCKER_HOST` pointing at a non-existent socket and at a `tcp://` address and checking exit code 1, the message, and that no alternate-screen sequence was written

## 3. Engine layer

- [x] 3.1 Define the `Engine` interface and the desk's value types; implement it over the Moby client with API negotiation (ping/version/info, list + inspect for containers, images, volumes, networks); verify it compiles and read-only calls return data from the real daemon via a throwaway read-only probe
- [x] 3.2 Implement mutations with no force parameter anywhere (container start/stop/restart/pause/unpause/kill/remove; image pull/remove; volume create/remove; network create/remove/connect/disconnect) and typed errors for not-found and conflict; verify by engine tests against the fake daemon (task 4)
- [x] 3.3 Implement streams: events, container logs (demultiplexed, TTY and non-TTY), container stats samples, pull progress; verify by engine tests against the fake daemon
- [x] 3.4 Implement exec create/attach/resize/inspect; verify by an engine test that runs a command in a fake container and reads its output
- [x] 3.5 Implement read-only mode (wrapper returning `ErrReadOnly` plus an HTTP request hook rejecting non-GET/HEAD); verify by a test that every mutating method fails and the fake daemon's request log contains no mutating request

## 4. Fake Docker daemon

- [x] 4.1 Implement the in-memory fake engine serving the Engine HTTP API on a unix socket (ping, version, info, lists, inspect, events) with seedable state and a request log; verify the real `docker` CLI can run `version`, `ps -a`, `images`, `volume ls`, `network ls`, `inspect` against it
- [x] 4.2 Add lifecycle, remove (409 when in use/running, 404 when missing), volume/network create/remove/connect/disconnect, image remove and pull progress, each emitting the events real Docker emits; verify with the real `docker` CLI (`stop`, `start`, `rm`, `rmi`, `network connect`, `pull`) against the fake
- [x] 4.3 Add logs (multiplexed framing, follow, configurable line rate for floods), stats streaming and exec with hijack and a scripted shell; verify `docker logs -f`, `docker stats --no-stream` and `docker exec` against the fake
- [x] 4.4 Add `cmd/fake-dockerd` with a demo seed (containers in various states, a compose project, dangling images, unused volumes/networks) and `make sandbox`; verify `DOCKER_HOST=unix://… docker ps` lists the seeded objects

## 5. Live-data primitives

- [x] 5.1 Implement the bounded ring buffer with version counter and visible-window read, the line splitter with maximum line length, and the log sanitiser; verify with unit tests (overflow drops oldest, long line split, escape sequences stripped except SGR) and the race detector
- [x] 5.2 Implement stats maths (CPU %, memory, network and block-I/O rates) and fixed-length history; verify with unit tests on recorded sample pairs
- [x] 5.3 Implement the stream supervisor: events stream with reconnect and interruption markers, one log stream for the current target, per-container stats streams with the 32-stream cap; verify with tests against the fake daemon (stream closed on target change, cap respected, reconnect after the fake drops the events connection)

## 6. Jobs

- [x] 6.1 Implement the job manager (states, progress, per-step log, cancel, bounded retention, clear finished, version counter); verify with unit tests including partial failure and cancellation mid-way

## 7. Planner (mutation safety)

- [x] 7.1 Implement plans for single-object actions with applicability checks, effect text, tiers and in-use blockers (running container, image used by containers, volume referenced, network attached/predefined); verify with table-driven tests covering every scenario in `specs/mutation-safety`
- [x] 7.2 Implement prune plans (images dangling/all-unused, volumes anonymous/all-unused, networks) with the conservative in-use rule and totals; verify with tests including "referenced only by a stopped container is not offered"
- [x] 7.3 Implement compose grouping and project stop/remove plans naming left-behind networks and volumes; verify with tests
- [x] 7.4 Implement plan execution as jobs (exact-set, continue on per-object failure, non-forced); verify against the fake daemon that an object made eligible after planning is untouched and an object made in-use after planning is reported, not deleted

## 8. Workspace UI

- [x] 8.1 Implement the root model, layout (wide, compact, too-small), header with daemon identity, footer hints, focus cycling and direct pane focus; verify with model tests on rendered frames at 140×40, 100×30, 80×24 and 60×20, and with `NO_COLOR`
- [x] 8.2 Implement the Kinds pane and List pane (tables per kind, counts, highlight preserved by identity across refresh, filter, empty and error states); verify with model tests driven by the fake daemon
- [x] 8.3 Implement the Details pane (info summaries for every kind, raw JSON inspect, tab switching, scrolling); verify with model tests
- [x] 8.4 Implement the Logs and Stats views over the stream supervisor (follow, scroll-back indicator, wrap, sparkline history, not-running states); verify with model tests against the fake daemon
- [x] 8.5 Implement the Events pane (formatting, highlighting of failure/destruction events, scroll-back and follow) and event-driven debounced refresh plus periodic poll with stale-response protection; verify with tests that a burst of events yields a bounded number of list requests in the fake's request log
- [x] 8.6 Implement the Jobs pane and job output viewer (progress, cancel, clear); verify with model tests
- [x] 8.7 Implement dialogs: confirm (`y`), typed confirm, blocked/refusal, prune preview with scope toggle and scrolling, forms (pull, create volume/network, shell command), container picker (connect/disconnect), action menu, help, quit-with-running-jobs; verify with model tests that Enter never confirms a `y` dialog and a mismatched word never proceeds
- [x] 8.8 Wire every action key for every kind through planner → dialog → job; verify with tests that for each mutating key the fake daemon's request log shows zero mutating requests until the confirmation is completed, and the expected request after it
- [x] 8.9 Implement connection-loss handling (disconnected indicator, stale marking, automatic recovery) and non-fatal error display; verify by stopping and restarting the fake daemon under a running model

## 9. Interactive shell

- [x] 9.1 Implement the exec session as a `tea.ExecCommand` (raw mode, bidirectional copy, SIGWINCH → resize, guaranteed terminal restore) and wire it to the shell form; verify with a pseudo-terminal test against the fake daemon that input reaches the shell, output comes back, and the terminal modes are restored after exit

## 10. Proof

- [x] 10.1 Add a guard so the test suite cannot reach the real daemon (tests force `DOCKER_HOST` to an invalid path and every test client targets a temporary socket); verify `go test ./...` passes with the guard and with `-race`
- [x] 10.2 Add the responsiveness test (log flood ≥ 50k lines/s plus stats and events; key-to-state latency under 100 ms; frame count bounded; buffers bounded); verify it passes and record the measured numbers in `docs/TESTING.md`
- [x] 10.3 Add the end-to-end pseudo-terminal harness that runs the real binary against the fake daemon and captures screens; verify a scripted session (navigate kinds, stop a container through its confirmation, see the event, run a pull job, open and leave a shell, quit) passes and the captured screens are saved under `docs/screens/`
- [x] 10.4 Run the real binary against the real daemon with `--read-only` under the pseudo-terminal harness, exercising every pane; verify the before/after snapshots of containers, images, volumes and networks on the real daemon are identical and record the comparison in `docs/TESTING.md`

## 11. Review and hardening

- [x] 11.0a Have the mutation path reviewed independently against the safety rules; verify each finding is either fixed with a test that replays the reviewer's sequence or kept with a recorded reason (docs/DECISIONS.md, B30)
- [x] 11.0b Drop a pending confirmation on any key, never open it over another overlay, discard stale answers, freeze a prune preview while it recomputes; verify with `TestAnswersToAbandonedRequestsDoNothing`
- [x] 11.0c Add the confirmation arming delay; verify with `TestConfirmationsMustBeReadFirst` and the end-to-end session
- [x] 11.0d Accept no key but quit while the terminal is too small; verify with `TestNothingActsWhileTheDeskCannotBeDrawn`
- [x] 11.0e Make the action menu act on the object it names; verify with `TestActionMenuActsOnTheObjectItNames`
- [x] 11.0f Pin image and volume steps addressed by name, keep the untag promise, guard prune untags against in-use; verify with the four pinning tests in `internal/plan`
- [x] 11.0g Bound shell start, add the local escape, end on SIGTERM/SIGHUP, reset terminal modes; verify with the pseudo-terminal tests in `internal/shell`
- [x] 11.0h Report a job that failed by itself as failed even if cancel was pressed, and count failures in a cancelled job's output; verify with `TestAFailureIsNotRelabelledAsCancelled` and `TestFailuresBeforeACancelAreNotHidden`

## 12. Finish

- [x] 12.1 Write `README.md` (what it is, install, launch, layout, key reference, safety model, sandbox, limits) and `docs/TESTING.md`; verify every command in the README was executed as written
- [x] 12.2 Complete `docs/DECISIONS.md` and `docs/JOURNAL.md` with decisions and findings made during the build; verify each decision records the alternatives and the reason
- [x] 12.3 Final gate: `go vet ./...`, `go test -race ./...`, `make build`, `go install` into a temporary `GOBIN`, launch check; verify all pass and `openspec validate docker-desk-workspace --strict` succeeds
