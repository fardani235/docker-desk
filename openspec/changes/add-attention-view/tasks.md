# Tasks

## 1. Classification: the ladder, ordering and counts

- [ ] 1.1 Add `internal/attention` with the class ladder and precedence from design D4 (dead, restarting, exited with a known code, paused/removing, created, running+unhealthy, running+restart count, running+healthy, unknown), and verify with a table-driven test covering every class and the precedence conflicts (paused+unhealthy, exited+unhealthy, running+no healthcheck).
- [ ] 1.2 Add the ordering and counts: needs-you order restarting, unhealthy, dead, exited by finish time (newest first); watch after; fine/unknown/not-a-problem as counts; verify with tests for mixed blocks and the empty block.
- [ ] 1.3 Add the transitional “reading” state for containers whose class needs an unread detail, so nothing is counted as fine or as needing you early; verify with tests that an unread exited container is neither, and settles on the answer.
- [ ] 1.4 Document the ladder and the three-restart threshold in the package comment, and verify `go test ./internal/attention/` passes.

## 2. Reads: one pass per daemon, focused first, event-driven refresh

- [ ] 2.1 Build the view's read run: each connected member's container list, then details only for candidates, per-member results under a lock with a version counter, cancellation on close, a per-member failure that keeps the rest usable; verify with tests over two fake daemons (both read, one fails, cancel stops reads).
- [ ] 2.2 Make the focused daemon's block come from the held snapshot immediately, reading it only if that list is not loaded; verify that the focused block is drawn before the other members answer.
- [ ] 2.3 Add the bounds: cap the detail inspects per member, fill rows as answers arrive, and report how many were not inspected; verify with a daemon holding more candidates than the bound.
- [ ] 2.4 Add event-driven invalidation: track each member's hub event position, re-read on new `die`, `oom` or `health_status`, debounce to one read per member per second, and send nothing on a quiet view; verify with a fake event and with a request-count test over a quiet interval.
- [ ] 2.5 Handle a member removed or disconnected while the view is open: cancel its read, show the reason, keep the other blocks; verify with session tests that remove and disconnect a member mid-read.

## 3. The Attention view: open, render, navigate, jump

- [ ] 3.1 Add `H` and the full-screen view: save and restore pane, kind and highlight; title with totals and read time; one block per member, focused first; rows and counts per design D9; hints and `?` help; verify with UI tests for open/close and restoration.
- [ ] 3.2 Render the empty answer, a single-daemon session, a disconnected member, long endpoints and a failed read without losing the rest of the view; verify with render tests including 80×24.
- [ ] 3.3 Add navigation and scrolling across blocks and rows at 80×24 and wider; verify with keyboard tests that the cursor survives a re-read and the list scrolls.
- [ ] 3.4 Add Enter's jump: close, focus the row's member, wait for the container with `JumpWait`, select it; on a gone container or disconnected member say so and restore the previous focus; verify with two-daemon UI tests for the happy path and both failures.
- [ ] 3.5 Add mouse rows and the wheel through the existing hit machinery; verify with the mouse tests that a click selects and a wheel scrolls, and that no click mutates anything.
- [ ] 3.6 Verify the view is read-only: every key it accepts sends no mutation, and the view opens under `--read-only`; verify with requests recorded by the fake daemons.

## 4. Ambient cue: health in the list and the count

- [ ] 4.1 Show the daemon's health word in the Containers STATE cell, warn-toned when not healthy, and invent nothing when the daemon reports none; verify with render tests for unhealthy, starting and no-healthcheck rows.
- [ ] 4.2 Add the count of dead + restarting + unhealthy containers to the Kinds entry and the Containers title, labelled as a subset that the view expands with exit codes and restarts; verify that it counts only those signals, updates with the list and clears.
- [ ] 4.3 Add `H` and the Attention description to the in-app help and the README key tables, plus a documentation test that both name the key and the view; verify with `go test ./internal/ui/ -run Documented`.

## 5. README and project records

- [ ] 5.1 Write the README Attention section: what the ladder means, the unknown/no-healthcheck rule, the read-on-open/`r`/event model with per-block ages, the jump and restore, and the explicit out-of-scope list; verify every key and sentence matches the built behavior and the documentation test passes.
- [ ] 5.2 Record the round in `docs/DECISIONS.md` (the design's decisions and any implementation discoveries) and `docs/TESTING.md` (commands run, results, and the absence of a real multi-daemon screenshot if e2e stays single-daemon); verify the entries exist and the commands were actually run.

## 6. Integration verification

- [ ] 6.1 Run `make test` and `go vet ./...` and confirm both pass with no new failures; record any pre-existing failure by name rather than treating it as this change's.
- [ ] 6.2 Walk the `container-attention` and `daemon-session` delta scenarios against the tests, and report any scenario without a covering test; add the missing test or record why it cannot be automated.

## Workflow follow-up

- Archive the change after review and after every task above is checked.
- Validate the archived specs with `openspec validate --all --strict` and fill any archived `Purpose` placeholder.
