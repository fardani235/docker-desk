# Tasks

## 1. The clone accepts the daemon's default volume mode

- [x] 1.1 Change `mountModeExtras` in `internal/plan/recreate.go` to take the mount type and accept a bare `z` for a `volume` mount, leaving `Z` on a volume and both `z` and `Z` on a bind as refusals; verify `go test ./internal/plan -run 'Clone'` and a new table case per combination: `z` volume carried, `Z` volume refused, `z` bind refused, `Z` bind refused as before.
- [x] 1.2 Change the named-volume fixture in `TestClonePortsAndStorage` (`internal/plan/recreate_test.go:252`) from `Mode: "rw"` to `Mode: "z"`, and add a fourth mount with `Mode: "ro,z"` and `RW: false`; verify the expected `MountSpec` list keeps every volume and marks the `ro,z` one read-only.
- [x] 1.3 Add the combined-mode case to the refusal table's sibling test (`TestCloneRefusesTheReviewedFamilies`) only for the tokens that still refuse, so the table names `mount option Z at …` for a volume and `mount option z at …` for a bind without duplicating the existing bind-`Z` case; verify both subtests fail if the type check is removed.
- [x] 1.4 Confirm no other family changed: verify `go test ./internal/plan -run 'Clone|RecreatePlan'` is green and the refusal counts in the test names are updated if the table gained a case.

## 2. The fake reports the daemon's mode

- [x] 2.1 Add `Mode string` to `fakeengine.MountSpec` (`internal/fakeengine/containers.go`), record it in the create handler for a `-v` spelling (`internal/fakeengine/create.go:258-280`, the third colon-separated part) and leave it empty for a typed `Mounts` entry, and make `mountPoints()` (`internal/fakeengine/containers.go:433`) report `z` for a volume whose mode is empty and whose driver is the fake's local driver; verify `go test ./internal/fakeengine` and a create-then-inspect test asserting `volume → z`, `-v …:ro → ro`, `-v …:Z → Z`, `--mount type=bind → ""`.
- [x] 2.2 Prove the re-create path against that shape: a test that creates a container with a named volume through `POST /containers/create`, inspects it, runs `plan.Clone` on the answer, and asserts no refusal and the volume carried by name; verify the test fails if 1.1 is reverted.
- [x] 2.3 Check the seeds and existing fake tests still pass, since seeded named volumes now report `z`; verify `go test ./internal/fakeengine ./internal/ui` is green.

## 3. Evidence against the real CLI and a real daemon

- [x] 3.1 Add `{{.Mode}}` to the mount formats of the create case (`scripts/cli-crosscheck.sh:206`) and the re-create case (`:252`) and record the daemon's answers for `--mount` volume (`z`), `--mount` bind read-only (empty) and `-v …:ro` (`ro`); verify `make crosscheck` passes with the new columns and the pre-existing failures are unchanged.
- [x] 3.2 Make the read-only real-daemon check report a skipped storage comparison instead of passing silently (`internal/plan/recreate_real_test.go:192`): log the refusal text when `FamStorage` is refused, and keep the per-mount comparison when it is not; verify `make real-readonly-check` against this machine's daemon and that the container list is unchanged before and after.
- [x] 3.3 Add an end-to-end case whose moved-tag container has a named volume (`internal/e2e/recreate_test.go`, a `SeedMovedTag` with `Volumes`), press `A`, type `recreate`, and assert the action is offered, the job completes and the volume is still mounted; verify `go test ./internal/e2e -run Recreate` and refresh the captured screens only if a visible line changed.

## 4. Documentation

- [x] 4.1 Update the README's carried/refused mount paragraph so it states that the daemon's default `z` on a named volume is carried by name and is not a refusal, while `Z` and a bind label still refuse; verify the documentation test and that the sentence names both remaining refusals.
- [x] 4.2 Add round twenty to `docs/DECISIONS.md` with the type-scoped rule, the decision not to carry a mode, the fake-fidelity change and the alternatives from `design.md`; verify the round number follows round nineteen and the introduction states what was left out.
- [x] 4.3 Add the proof rows to `docs/TESTING.md`: the clone tables, the fake's reported default, the cross-check's mode column and the read-only check's logged skip, with the commands the suites use; verify each named test and command exists.

## 5. Integration

- [x] 5.1 Run `make build`, `make vet` and gofmt over the touched files; verify all are clean.
- [x] 5.2 Run `go test ./...` and `go test -race ./internal/plan ./internal/fakeengine ./internal/ui`; verify green and record any pre-existing flake instead of accepting it silently.
- [x] 5.3 Run `make e2e` and `make crosscheck`; verify the re-create screens and the mode columns pass.
- [x] 5.4 Run `openspec validate fix-recreate-default-volume-mode --strict`; verify it is valid.

## Workflow follow-up

- Archive the change after the review requirements are satisfied.
- Verify `openspec/specs/container-recreation/spec.md` carries the two modified requirements and the new scenarios, and that the README, DECISIONS and TESTING agree.
