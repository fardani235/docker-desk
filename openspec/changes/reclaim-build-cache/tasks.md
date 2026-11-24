# Tasks

## 1. Engine and fake daemon

- [ ] 1.1 Add `BuildCachePruneReport` (deleted IDs, reclaimed bytes), `BuildCachePrune(ctx, all)` to `engine.Engine`, its moby implementation (`POST /build/prune`, only `All` set), and its `readOnlyEngine` refusal; amend the interface comment for this one set-acting mutation. Verify `go test ./internal/engine -run BuildCache` compiles and the read-only test sees `ErrReadOnly` with no request.
- [ ] 1.2 Implement `POST /build/prune` in the fake engine: read `all`, remove every non-`InUse` record (and `internal`/`frontend` types only with `all`), answer the daemon's report shape, and record the request. Verify engine tests assert one POST, the report's IDs and bytes, the default/all difference, and that an in-use record survives.
- [ ] 1.3 Extend `scripts/cli-crosscheck.sh` with `docker builder prune` (default and all) against the fake and assert the CLI's result. Verify `make crosscheck` passes, or skips explicitly when the CLI has no buildx plugin.

## 2. Plan and confirmation body

- [ ] 2.1 Add `OpPruneBuildCache`, the build-cache plan builder, and the display-only `Plan.BuildCache` (candidates, kept count, scope, account time, derived flag, reclaimable). Verify plan tests: one step, tier typed with word `prune`, default excludes in-use and `internal`/`frontend`, all includes those types, shared flags preserved, `Nothing` when no candidate.
- [ ] 2.2 Add `OpPruneBuildCache` to `Execute`: one request, log the daemon's deleted count/IDs and reclaimed bytes, compare with the preview and log extras and kept records, and never claim `Step.Size`. Verify execution tests with the fake for success, difference and refusal.
- [ ] 2.3 Render the build-cache preview in `planBody` through the existing scrolling path, with the "the daemon decides the final set" note and no "Exactly these N" wording. Verify a UI test shows every candidate, scrolls a long list, and never says the set is exact.

## 3. Space action

- [ ] 3.1 Add `b` to the Space keys and hints, omitted in read-only mode, and `prepareBuildCachePrune` that reads `GET /system/df` only (bounded, plan-sequence guarded) and opens the plan. Verify tests: `b` sends only the account GET and no mutation; `b` in read-only opens nothing.
- [ ] 3.2 Wire `Tab` in the build-cache dialog to a fresh account read for the other scope, clearing the typed word and refusing consent until the new preview is drawn. Verify tests for the scope switch, the account being read twice, and the recomputing guard.
- [ ] 3.3 Confirm the typed `prune`, send exactly one prune, log the report, mark the Space account stale and start a fresh account read when the job finishes with Space open. Verify an end-to-end model test with the fake: one POST, no other request, the job line, and a refreshed account.

## 4. Safety and result

- [ ] 4.1 Prove the promise boundaries: in-use records never appear as candidates, no force or filters are sent, and the read-only engine refuses the prune even if the UI is bypassed. Verify focused tests for each.
- [ ] 4.2 Verify the report honesty: a record deleted but not previewed is logged as such, a previewed record kept is logged, and a daemon refusal fails the job with the daemon's message while the account is re-read.

## 5. Documentation, end to end, and integration

- [ ] 5.1 Update the README's Space section, its build-cache paragraph and its "What it does not do" list; add the `b` key to the Space walkthrough and note the daemon's discretion. Regenerate the Space screen captures and verify the diff shows only the intended changes.
- [ ] 5.2 Record the decision in `docs/DECISIONS.md`, update `docs/TESTING.md`'s build-cache coverage, and amend the main `mutation-safety` Purpose so it no longer claims every prune is exact. Verify the cross-references and numbering.
- [ ] 5.3 Add pty tests: `S`, `b`, `Tab`, type `prune`, confirm; assert one build-cache POST, the report on screen, and the refreshed account. Run read-only with `b` and assert no dialog and no request; capture the new screens.
- [ ] 5.4 Run `go test ./...`, `go test -race ./...`, `make crosscheck` and `make e2e`; verify the full suite, the CLI cross-check and the captures are green. Add an opt-in real-daemon read-only check only if it needs no mutation.

## Workflow follow-up

- Archive the change after review.
- Validate the archived change and the updated specs.
