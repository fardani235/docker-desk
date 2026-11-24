# Tasks

## 1. Engine and fake daemon

- [ ] 1.1 Add `PushImage(ctx, ref, registryAuth string, progress func(PushProgress)) (PushResult, error)` to `engine.Engine`, implement it with moby `ImagePush` (`All: false`, `X-Registry-Auth`, JSON progress aggregation, final digest), and make `readOnlyEngine` refuse it. Verify `go test ./internal/engine -run Push` covers the request, auth header, progress, digest and `ErrReadOnly`, and that no credential appears in an error.
- [ ] 1.2 Add `POST /images/{name}/push` to the fake: tag query, image lookup, recorded request/auth, streamed progress (with a delay hook for cancel), final digest, and daemon-shaped errors. Verify engine tests and extend `scripts/cli-crosscheck.sh` with `docker push`; run `make crosscheck`.

## 2. Plan and execution

- [ ] 2.1 Add `OpPushImage` and `plan.Push(snap, imageID, ref)` with the typed tag, the image-ID pin, local-unchanged and remote-create/move effects, and a `NotApplicable` for an untagged row or a reference that is not the image's. Verify plan tests for the word, one step, pin, effects, untagged refusal and a ref that does not belong to the image.
- [ ] 2.2 Extend `Plan` with `CredSource` and a `RegistryAuth` callback (never displayed) and execute `OpPushImage`: pin first, resolve the credential, call `PushImage`, log progress and the daemon's digest, and abort with nothing sent on a moved tag, a missing image or a helper failure. Verify execution tests for success, moved tag, helper failure, cancellation and refusal.

## 3. Action, credential source and dialog

- [ ] 3.1 Add the Images `p` action (menu and direct key), the one-at-a-time and untagged refusals, read-only refusal through the existing guard, and `--no-registry` refusal. Verify model tests for each, and that no request is sent before the typed confirmation.
- [ ] 3.2 Add `preparePush`: snapshot, reference validation, one `registry.ResolveCredential` for the source (a helper failure becomes a blocker with no secret in it), the `RegistryAuth` closure, and a warning when the focused daemon is reached over unencrypted `tcp://`. Verify tests for a stored credential source, no credential, a helper failure, an unencrypted link, and that the dialog and job contain no credential value.
- [ ] 3.3 Render the confirmation: full reference, short ID, registry, credential source, upload/remote-tag/local-unchanged effects, the unencrypted-link warning, and the typed tag. Verify render tests and the wrong-word/`y`/arming behavior.

## 4. Job behavior

- [ ] 4.1 Implement the push job reporting layer progress and the daemon's final digest, with the cancellation line (uploaded layers may remain; the tag may not have moved) and the refusal line (local image and tag unchanged). Verify UI tests with the fake for progress, success, cancel and registry refusal.

## 5. Documentation, end to end, and integration

- [ ] 5.1 Update the README's registry round, Images keys and "What it does not do" paragraph; add `p` push, the typed tag, credential source, read-only/`--no-registry` rules and the unencrypted-link warning. Record the decision in `docs/DECISIONS.md`, update `docs/TESTING.md`, and regenerate the Images screens.
- [ ] 5.2 Add pty tests: select a tag, `p`, read the confirmation, type the tag, confirm, and assert one push request with the right tag and `X-Registry-Auth`, progress and the final digest on screen; run read-only and `--no-registry` and assert no push. Capture the new screens.
- [ ] 5.3 Run `go test ./...`, `go test -race ./...`, `make crosscheck` and `make e2e`; verify the full suite, the race run, the CLI cross-check and the captures are green.

## Workflow follow-up

- Archive the change after review.
- Validate the archived change and the new capability.
