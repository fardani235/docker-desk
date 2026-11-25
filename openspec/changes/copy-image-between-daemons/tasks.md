# Tasks

## 1. Engine: load an archive into a daemon

- [ ] 1.1 Add `LoadProgress`, `LoadResult` and `LoadImage(ctx, content, platform, progress)` to `internal/engine/engine.go`, implement it in `internal/engine/moby.go` (raw `application/x-tar` POST, JSON stream decoding, "Loaded image"/"Loaded image ID" lines, daemon error mapping, API ≥1.48 platform guard), and refuse it in `internal/engine/readonly.go`; verify with `go test ./internal/engine/ -run 'Load|ReadOnlyCoversEveryMutation'`.
- [ ] 1.2 Add `POST /images/load` to `internal/fakeengine` (read the tar, parse `manifest.json` and the config's `rootfs.diff_ids`, register the image and tags, move an existing tag, emit load JSON, honour a settable chunk delay and an injected failure); verify with a fake-engine test that loads a fixture archive, moves a tag, rejects a malformed tar and reports an injected failure.
- [ ] 1.3 Add engine-level load tests over a stub HTTP server: correct method/path/content type, the source body reaches the daemon unchanged, progress decoding, an error message becomes `*engine.Error`, and cancelling the context closes the request; verify with `go test ./internal/engine/ -run Load`.
- [ ] 1.4 Document the method's contract in the `Engine` interface comment (one archive, one platform, read-write, cancellation) and confirm `go vet ./internal/engine/` passes.

## 2. Plan: the copy plan and its two-engine executor

- [ ] 2.1 Add `CopySpec`, `Plan.Copy`, `OpCopyImage` and `plan.CopyImage(snap, imageID, ref, platform, destination)` with the typed word (tag or short ID), effects, warnings (insecure links) and blockers (target already holds the same image, target read failed); verify with unit tests for each branch in `internal/plan/copy_test.go`.
- [ ] 2.2 Implement `plan.ExecuteCopy(ctx, src, dst, p, run)` in `internal/plan/copy_exec.go`: source `ImageFacts` pin check, target reference re-check against the dialog's owner, direct `SaveImage`→`LoadImage` stream with byte progress and target status, cancellation closing both sides, layered notes, and post-load `ImageFacts` verification of ordered diff IDs and platform; verify with tests over two fake engines (success, cross-store IDs, tag moved, target changed, source error, target error, cancel, verification mismatch).
- [ ] 2.3 Make `plan.Execute` refuse a copy plan and keep `Runnable`/validation honest for `Plan.Copy`; verify with a test that `Execute` returns an error naming the two-daemon path and sends nothing.
- [ ] 2.4 Add a test asserting the request set of one copy: exactly one `GET /images/get` on the source, one `POST /images/load` and the verification read on the target, and no file created on this machine; verify with the fake engines' recorded requests.

## 3. Jobs and session: one job spanning two daemons

- [ ] 3.1 Add `Job.From`, `jobs.Manager.StartOnPair(target, source, …)` and make `CloseDaemon`/`RunningOn` match either daemon; verify with `go test ./internal/jobs/ -run 'Pair|Daemon'`.
- [ ] 3.2 Show "on <target> from <source>" in the Jobs pane and the status line when `From` is set, leaving single-daemon jobs unchanged; verify with a UI test that renders a two-daemon job.
- [ ] 3.3 Verify `Session.Remove` refuses removal of either daemon of a running copy and names the job; add the session test that removes each side in turn.

## 4. UI: action, destination chooser, confirmation and job

- [ ] 4.1 Add Images' `C` action to `kindActions` (`mutating`, `needsRow`, no `setCapable`), add `disabled`/`reason` to `pickItem` with dim rendering and Enter ignored, and verify: one daemon refuses with the `M` hint, a selection refuses, read-only refuses before any request, and `--no-registry` does not refuse.
- [ ] 4.2 Implement the destination chooser and the pre-dialog reads (source `plan.Take` + `ImageFacts`, target `ImageFacts`), then build the plan through `plan.CopyImage`; verify with tests for two connected daemons, a disconnected target shown unusable, a target already holding the image (blocked), and a target tag naming another image (stated as moved).
- [ ] 4.3 Render the copy confirmation with separate source and target identity lines (name, endpoint, context, link tag), the referenced object, platform and tag effect, reusing `daemonLines` through an endpoint/link parameter; verify with tests for the wrong typed word, `y` not being consent, a removed target while the dialog is open, long endpoints, and the smallest terminal.
- [ ] 4.4 Dispatch `runPlan` for a copy plan: look up both members' engines at job start, start via `StartOnPair`, and announce success/failure with both daemons; verify with a two-fake-daemon UI test that the image appears on the target, the source is unchanged, the job shows both names and bytes, a cancel closes both streams, and a target removed mid-run is refused.
- [ ] 4.5 Add the action to the in-app help and action menu, and add a documentation test that the README Images row and the help both name `C` and the copy description; verify with `go test ./internal/ui/ -run 'Copy|Documented'`.

## 5. Documentation

- [ ] 5.1 Update README: the Images key row and action table, a "Copy" section beside Save and Push (stream, no file, tag effect, platforms, cancel note), "Several daemons in one session" (one action may name two members), and "What it does not do" (images leave the cross-daemon limitation; volumes and containers stay; no multi-platform, retag, resume or bulk copy); verify every command and key in the new text matches the built binary and `go test ./internal/ui/ -run Documented`.
- [ ] 5.2 Record the round in `docs/DECISIONS.md` (D1–D11 from design.md and any implementation discoveries) and `docs/TESTING.md` (commands run, results, and the absence of a real-daemon load test with the reason); verify the entries are present and the commands they list were actually run.

## 6. Integration verification

- [ ] 6.1 Run `make test` and `go vet ./...` and confirm both pass with no new failures; record any pre-existing failure by name rather than treating it as this change's.
- [ ] 6.2 Walk the `image-copy` spec scenarios against the tests and report any scenario with no covering test; add the missing test or record why it cannot be automated (for example a real-daemon load, forbidden by the test guard).

## Workflow follow-up

- Archive the change after review and after every task above is checked.
- Validate the archived specs with `openspec validate --all --strict` and fill any archived `Purpose` placeholder.
