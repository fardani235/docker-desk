# Tasks

## 1. Parsing and the create plan

- [ ] 1.1 Move the quote-aware command splitter from `internal/ui/dialog.go` into `internal/plan` as `SplitCommand`, point the shell form at it, and add table tests for quotes, escapes, empty input and an unterminated quote; verify `go test ./internal/plan ./internal/ui` passes.
- [ ] 1.2 Add `plan.ContainerCreateSpec` and the pure parsers `ParsePort`, `ParseEnv`, `ParseMount`, `ParseRestart` with table tests: valid forms, tcp default, `udp`, bracketed IPv6, out-of-range port, missing `=`, relative destination, unknown restart policy, `on-failure:3`; verify `go test ./internal/plan -run 'Parse|Split'`.
- [ ] 1.3 Add `OpCreateContainer`, `Step.Create`, `Step.DependsOn`, `Step.ProducesID`/`Step.IDFrom` and `plan.ContainerCreate(snap, spec)`; tests cover the pull step present/absent, dependency indices, `:latest` normalization, a name already used, a repeated host port, and `describe()` carrying no environment values; verify `go test ./internal/plan`.

## 2. Execution: dependencies and the produced ID

- [ ] 2.1 Extend `plan.Execute` with a per-step outcome and the dependency check, skipping a step whose prerequisite did not succeed, and carry the create step's returned ID into the start step; verify with tests in `internal/plan/execute_test.go` for a failed pull (create and start skipped), a failed create (start skipped), and a rename between create and start.
- [ ] 2.2 Report skipped steps in the job: log each skip with the step it waited on and count succeeded, failed and skipped in the final summary; verify a test asserts the job's output lines and its failed state.
- [ ] 2.3 Verify existing plans are unchanged: `go test ./internal/plan` passes for prunes, projects and reference pins, including the "one failure does not stop the rest" cases.

## 3. Engine and the fake daemon

- [ ] 3.1 Add `engine.ContainerCreateOptions` and `CreateContainer(ctx, opts) (string, error)`, mapping image, name, ports (`ExposedPorts`/`PortBindings`), env, mounts (`mount.Mount` with an explicit type), restart policy and command onto `cli.ContainerCreate`; verify an engine test against the fake asserts the decoded request body and the returned ID.
- [ ] 3.2 Add the `readonly.go` override returning `ErrReadOnly` and bump the hard-coded count in `readonly_test.go` to 19; verify `go test ./internal/engine -run TestReadOnlyCoversEveryMutation` passes and a read-only create sends nothing.
- [ ] 3.3 Implement `POST /containers/create` in `internal/fakeengine` (404 for a missing image, 409 for a taken name, 201 with the ID, a `created` container carrying the spec, an emitted `container`/`create` event); verify `go test ./internal/fakeengine` and that the existing start handler runs the created container.
- [ ] 3.4 Add a create section to `scripts/cli-crosscheck.sh` (`docker create` with name, port, env, mount, restart policy and command, inspect the result, then `docker start`), proving the fake against the real CLI; verify `make crosscheck` passes on a machine with the `docker` CLI.

## 4. The form and the action

- [ ] 4.1 Generalise `dlgForm` to `fields []formField` (text, list, choice) and port the pull, volume, network and shell forms onto it as single-field forms; verify the existing UI behaviour tests still pass and new tests cover `Tab`/`Shift+Tab` wrapping and `Esc` closing without a job.
- [ ] 4.2 Implement repeatable rows: `↑`/`↓` between entries, `Ctrl+N` to append, `Ctrl+X` to remove the focused entry (clearing it when it is the last); verify tests add two ports, remove one, and confirm `Ctrl+N`/`Ctrl+X` never reach the text input.
- [ ] 4.3 Build the create form with its seven fields, the defaults (no ports/env/mounts, `no` restart policy, image command), the per-field syntax hints and the live summary naming the daemon, the normalized image, whether the last image list has it, and every value; verify a UI test reads the summary before submitting.
- [ ] 4.4 Wire submission: parse the values into a `ContainerCreateSpec`, build the plan from a fresh snapshot, keep the form open with the error and focus on the offending field when a value is refused, and ignore `Enter` before the form is armed; verify tests for a bad port, a taken name, a malformed env entry, a relative destination and a typed-ahead `Enter`.
- [ ] 4.5 Make the form body scroll within `maxBody`, keeping the focused field and the hint line visible and showing `field i of n`; verify a layout test at 80×24 with every field and three entries in each list still draws the hint line and the focused field.
- [ ] 4.6 Add the `n` action (`new…`, mutating, `ignoresSet`) to the Containers kind, with its action-menu entry, footer hint and help text; verify UI tests open it with and without a selection, and that read-only refuses it with the standard message and sends nothing.
- [ ] 4.7 Keep environment values out of the job record: the job title and target name the container or image, and the step description never includes the spec; verify a test creates with `TOKEN=s3cret` and asserts the job's output and the status line do not contain the value while the form does.

## 5. Documentation and the captured screen

- [ ] 5.1 Record the decisions as D23–D30 in `docs/DECISIONS.md` (the step chain and dependencies, the spec/options split, parsing in `plan`, the field model, the scrolling form and summary, the engine method and fake endpoint, environment values, the event-driven refresh), keeping each entry's alternatives and cost; verify each heading is sequential and matches `design.md`.
- [ ] 5.2 Document the new key and what it creates in `README.md` (the key table and the containers paragraph), including create-and-start and the implicit pull; verify the documented key text appears in the in-app help and any documentation test still passes.
- [ ] 5.3 Capture the create form into `docs/screens/` following the existing captures' naming and size, and reference it where the other screens are referenced; verify the file renders and the README link resolves.
- [ ] 5.4 Add the create flow to `docs/TESTING.md`'s proof table (pure parser/plan tests, the fake daemon, the UI model, the CLI cross-check); verify the commands named there are the ones the tests actually use.

## 6. Integration

- [ ] 6.1 Add an end-to-end test in `internal/e2e` that creates a container from an image the sandbox does not have, and asserts the pull happens first, the container appears in the list as running, and the job succeeds; verify `go test ./internal/e2e -run Create`.
- [ ] 6.2 Add an end-to-end case for an image already present (no pull request) and one for a failing pull (no container created, nothing started); verify the fake's request log is the witness in both.
- [ ] 6.3 Run `make build`, `make vet` and `go test ./...` and resolve every regression; record anything the suite reports that predates this change instead of silently accepting it.

## Workflow follow-up

- Archive the change after the project's review requirements are satisfied.
- Verify the archived specs and the main capability specs agree after archive.
