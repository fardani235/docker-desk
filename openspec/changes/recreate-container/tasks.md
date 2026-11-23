# Tasks

## 1. The clone derivation

- [x] 1.1 Create `internal/plan/recreate.go` with `CloneSpec` and a narrow struct for the fields parsed from `engine.ContainerDetail.Raw`; map identity, process, environment, health and stop settings; verify `go test ./internal/plan` with a fixture detail asserting entrypoint, command, working directory, user, hostname, TTY, init, labels, health check, stop signal and timeout, and every `NAME=value` pair.
- [x] 1.2 Map ports and storage: published bindings with host address, host port and protocol, exposed-only ports into `ExposedOnly`, bind and volume mounts with the read-only flag, and the volume name for anonymous volumes; verify a test where an inspect port without a binding produces no published port, and an anonymous volume keeps the daemon's name.
- [x] 1.3 Map networking: the network mode for default, host and none, every user-defined attachment with its aliases, and no fixed addresses; verify a test with two attachments and aliases, and a refusal for `container:<id>`.
- [x] 1.4 Map the restart policy with its retry limit, memory, swap, reservation, NanoCPUs, CPU shares, quota, period, cpuset, pids limit and shared memory size; verify a table test asserting each carried value and that an unset value stays unset.
- [x] 1.5 Implement the refusal families from design.md (privilege, capabilities, devices and device requests, security options, runtime, sysctls and ulimits, tmpfs, host and DNS overrides, links, log configuration, read-only root filesystem, auto-remove, namespace modes and cgroup parent, a fixed network address, and a mount type other than bind or volume); verify a table test with one container per family, and one with three families asserting all three are named.
- [x] 1.6 Implement `PreviousName` with the `<name>-previous-<short id>` rule, truncation to the name bound, `-2`, `-3` disambiguation against the snapshot, and validation with `validName`; verify tests for a free name, a taken name, a long name and a name that needs a third choice.
- [x] 1.7 Assert the derivation never puts an environment value into an error: verify a test whose container has `TOKEN=s3cret` and an unsupported setting, failing if the error contains the value.
- [x] 1.8 Review every field of the pinned `container.HostConfig` and `container.Config` against the carried and refused tables, add any meaningful missing family, and record the review in `docs/DECISIONS.md`; verify a test for each added family and a note in the round stating the limitation that a future field is not detected.

## 2. Engine, fake daemon and the wire shape

- [x] 2.1 Add `HealthConfig`, `NetworkAttachment` and `ResourceLimits` to `internal/engine/types.go`, and the new optional fields to `ContainerCreateOptions`; verify `go build ./...` and that no existing test changes, so the form's request is unchanged.
- [x] 2.2 Map the new options in `createConfig` into `container.Config`, `container.HostConfig` and the request's `NetworkingConfig`, including `ExposedOnly` without a port binding; verify engine tests that decode the fake's received create body and compare every field, and that an all-zero options value sends the same body as before.
- [x] 2.3 Make the fake's create handler record the new fields and its inspect answer report them (entrypoint, labels, TTY, working directory, user, hostname, init, health, stop signal and timeout, limits, shared memory, exposed-only ports, networks with aliases); verify a create-then-inspect test that compares each field and a second re-create derivation from that answer.
- [x] 2.4 Add `POST /containers/{id}/rename` to the fake: 204 with the name changed and the event emitted, 409 for a name in use, 400 for an invalid name, 404 for an unknown container; verify fake-engine tests and that `Requests()` records the call.
- [x] 2.5 Add `RenameContainer` to `Engine` and its moby implementation; override it in `readonly.go` and raise the coverage count to 21; verify `go test ./internal/engine -run ReadOnly` and that read-only leaves the name unchanged.
- [x] 2.6 Add a `docker create` case with the clone fields and a `docker rename` case to `scripts/cli-crosscheck.sh`, comparing `docker inspect` field by field; verify the new section passes with the real CLI and record the three pre-existing red checks instead of fixing them here.

## 3. The plan and its execution

- [x] 3.1 Add `PinRef` and `PinID` to `plan.Plan` and check them in `Execute` before the first step; verify a plan test where the pinned reference now names another image and the fake received no request at all.
- [x] 3.2 Implement `ContainerRecreate` with exactly stop (only when active), rename, create and start, using `DependsOn`, `ProducesID` and `IDFrom`; verify a table test asserting the step operations, dependencies and that no step is a removal.
- [x] 3.3 Implements the plan's checks: the original name is checked against every other container and not the re-created one, each attached network exists in the snapshot, and the derived name is free; verify tests for a taken original name, a missing network and a derived-name collision.
- [x] 3.4 Build the plan's title, target, effects, warnings and blockers: the four steps with the derived name, both image references and short IDs, "nothing is deleted", the bind-source and shell warnings, the compose consequence, and one blocker per refused family; verify a rendering test and assert no environment value appears in any of them.
- [x] 3.5 Execute the plan against the fake for the success path, asserting the old container is stopped and renamed and the new one runs under the original name; verify the fake's request log and the job log name both containers.
- [x] 3.6 Execute each failure path — stop, rename, create and start refused, and a cancel after the rename — asserting the skipped dependents and the state named; verify one test per path against injected fake errors.
- [x] 3.7 Make `describe` cover the rename operation and confirm the create step's description still names only the image and the ID; verify a plan test with a sentinel environment value across the job title, target and every log line.

## 4. The desk

- [x] 4.1 Add `prepareRecreate` as one `tea.Cmd` performing the reads from design.md (snapshot, inspect, pair, both image facts, platform, clone) and mapping refusals to `NotApplicable` for a pair that cannot move and to a blocked plan for clone settings; verify a UI test with the fake for each outcome.
- [x] 4.2 Add `A` to `onChangesKey`, refusing in read-only mode before any request and refusing while the container kind has a selection; verify tests that no request is recorded in either case.
- [x] 4.3 Gate the offer: moved verdict, both facts, same platform, the container still holding its name, and a clone with no refusal, cached per container and inspect revision; replace `redeployNote` with the verdict-dependent note, add the key to the tab footer and the help text; verify rendering tests for the offered case, the refused setting case, the taken-name case and the pinned-reference case.
- [x] 4.4 Render the confirmation with the target and short ID, the daemon, both image references and short IDs, the derived previous name, the effects, the warnings, the blockers and the typed word `recreate`; verify a UI test reading the dialog and a test that Enter or `y` sends nothing.
- [x] 4.5 Verify environment secrecy end to end: a sentinel value in a container's environment must not appear in the tab, the dialog, the job title, target or log, the status line, or the error of a refused create.
- [x] 4.6 After the job, verify the new row's Changes tab says the tag names its image and offers nothing, and the old row's tab says its name now belongs to another container and offers nothing.
- [x] 4.7 Add an end-to-end test that opens a container's Changes tab on a moved image, presses `A`, types `recreate`, and asserts the list, the job and the derived name; capture the dialog and a refusal at 100 and 80 columns into `docs/screens/` and add the links to the README.

## 5. Evidence and documentation

- [x] 5.1 Update the README: replace the "which this desk does not do" sentence in the Changes section, add `A` to the Keys table, add the typed word to the safety model, and change "What it does not do" to the refusal families and the old-container consequence; verify the documentation test and that the documented key matches the help text.
- [x] 5.2 Add round nineteen to `docs/DECISIONS.md` with these decisions and their alternatives, including the previous-name rule, the closed model and its limitation, the environment-value rule, the plan-level pin and the typed confirmation; verify the headings are sequential and the round's introduction states what was left out.
- [x] 5.3 Add the proof table and the "not proven" statement to `docs/TESTING.md`: derivation, plan, fake, cross-check and the read-only real-daemon check, and that no re-create has been sent to a real daemon; verify the commands named are the ones the suites use.
- [x] 5.4 Add the read-only real-daemon check that derives a spec from a real container's inspect and asserts every carried field equals the source, skipping without a daemon; verify `make real-readonly-check` on this machine and that the daemon's objects are unchanged.
- [x] 5.5 Verify the captured screens render at both widths and every README link resolves.

## 6. Integration

- [x] 6.1 Run `make build`, `make vet` and `gofmt -l .`; verify all three are clean.
- [x] 6.2 Run `go test ./...` and `go test -race ./internal/plan ./internal/engine ./internal/ui`; verify the new packages are race-clean and record any pre-existing flake instead of accepting it silently.
- [x] 6.3 Run `make crosscheck` with the real `docker` CLI; verify the new create and rename sections pass and the three pre-existing red checks are unchanged.
- [x] 6.4 Run the targeted end-to-end tests and the existing create and copy tests; verify the create form's request and behaviour are unchanged.
- [x] 6.5 Run `openspec validate recreate-container --strict`; verify it is valid.

## Workflow follow-up

- Archive the change after the project's review requirements are satisfied.
- Verify the archived specs and the main capability specs agree after archive.
