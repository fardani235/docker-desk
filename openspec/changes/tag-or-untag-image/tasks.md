# Tasks

## 1. Engine and fake daemon

- [x] 1.1 Add `TagImage(ctx, source, target)` to `engine.Engine`, implement it with moby `ImageTag`, and make `readOnlyEngine` refuse it. Verify engine tests cover one request with `repo`/`tag`, the source by ID, the read-only refusal and a missing source.
- [x] 1.2 Add `POST /images/{name}/tag` to the fake: normalise the target, move it from any other image, append it to the source, record the request, emit the image `tag` event and answer 404 for a missing source. Verify engine tests and extend `scripts/cli-crosscheck.sh` with `docker tag`; run `make crosscheck`.

## 2. Plan helpers

- [x] 2.1 Add `plan.NormalizeTag`: reject empty, spaces, invalid and digest targets, fill `:latest`, and return the familiar `name:tag`. Verify a table test for each case, including a registry host and port.
- [x] 2.2 Add a current-owner lookup for the normalised target over the image snapshot (none / this image / another image with its short ID). Verify tests for all three and a target that is also a repository tag of this image.
- [x] 2.3 Add `plan.ImageUntag`: refuse a missing image/reference and the last reference with `NotApplicable`; otherwise build the `KeepsData` removal step pinned to the image ID, size 0, typed word = the tag, effects naming the remaining references. Verify plan and execution tests for last-ref, untag success, moved reference, and a reference that became the last one.

## 3. The two actions

- [x] 3.1 Add the `t` action and `formTag`: source image ID/name/refs, one target field, live normalisation/error display, and Enter starting one tag job by ID. Verify no request before Enter, `acme/api` → `acme/api:latest`, digest/invalid refusal, untagged source, read-only and selection refusal, and no registry request.
- [x] 3.2 Show the target's current owner: create, already-this-image (Enter sends nothing), or another image with the move warning. Re-inspect the target in the job and refuse if it changed from what the form showed; on success mark Images for refresh and verify the fake's tag event. Verify tests for each branch, including absent→another-image.
- [x] 3.3 Add the `u` action and `prepareUntag`: refuse an untagged row and the last reference with the `d remove` pointer; otherwise open the typed-tag plan and remove only that reference. Verify success keeps the image under its other references, wrong word sends nothing, the tag moving refuses, and read-only/selection refusal.

## 4. Safety

- [x] 4.1 Prove the local boundary: neither action sends a registry request under `--no-registry`, neither forces, the tag source is the image ID, and untag cannot become a deletion (last-ref refusal and `KeepsData` re-check). Verify focused tests for each.

## 5. Documentation, end to end, and integration

- [x] 5.1 Update the README's Images keys and action sections with `t`/`u`, the normalisation, move warning, last-tag rule and local-only behavior; update the image-operations Purpose in the main spec at apply time. Record the decision in `docs/DECISIONS.md`, update `docs/TESTING.md`, and regenerate the Images screen captures.
- [x] 5.2 Add pty tests: tag an image and see the new row; untag a multi-tagged image and keep the image; the last tag untag refuses; read-only and `--no-registry` behavior. Capture the new screens.
- [ ] 5.3 Run `go test ./...`, `go test -race -p 1 ./...`, `make crosscheck` and `make e2e`; verify the full suite, race run, CLI cross-check and captures are green.

## Workflow follow-up

- Archive the change after review.
- Validate the archived change and the updated `image-operations` spec.
