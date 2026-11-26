# Tasks

## 1. State file package

- [ ] 1.1 Create `internal/state` with the version-1 `File` and member types, encoding only `version`, `theme`, `focus`, and each member's `endpoint`, `context` and `readOnly`; decode strictly and reject unknown fields, unknown versions, missing endpoints and wrong field types. Verify table-driven `internal/state` tests cover each rejection.
- [ ] 1.2 Resolve the path from `os.UserConfigDir` with `$XDG_CONFIG_HOME` override and create `docker-desk/` with mode `0700`; verify tests set a temporary XDG directory and assert the resulting path and directory mode.
- [ ] 1.3 Implement safe loading: a missing file is not an error, and a symlink, a non-regular file, a file not owned by the current user, a group/world-writable file, or unreadable content is a classified failure. Verify tests for symlink, mode `0666`, truncated JSON and an absent file.
- [ ] 1.4 Implement atomic saving: exclusive temporary file `0600`, write, sync, rename over the path, directory sync; a load failure disables saving for the run. Verify tests assert file mode, replacement of an existing symlink, no partial file on error, and that `Enabled` is false after a bad load.
- [ ] 1.5 Document the package's trust boundary in its package comment and verify `go test ./internal/state` passes.

## 2. Engine mode

- [ ] 2.1 Add `ReadOnly() bool` and `SetReadOnly(bool)` to `engine.Engine`; make `engine.New` always return a guard whose wrapper and HTTP hook share one atomic flag initialised from `Options.ReadOnly`. Verify the existing read-only suite passes and `TestReadOnlyCoversEveryMutation` includes the two new read methods and their updated count.
- [ ] 2.2 Add tests that start read-write, enable the mode, and see a mutating request refused by the wrapper and by the hook with no mutation at the fake daemon; then disable it and see the same call reach the daemon. Verify `go test ./internal/engine -run ReadOnly`.
- [ ] 2.3 Compile and adjust the embedders and wrappers used by `plan`, `shell`, `space` and `ui` tests; verify `go test ./internal/engine ./internal/plan ./internal/shell ./internal/space` passes with no interface changes leaking into call sites.

## 3. Session per-member modes

- [ ] 3.1 Replace `Config.ReadOnly` with `Config.ReadOnlyLock`, take an intended read-only mode in `Add` and `AddLaunch`, and drop `ReadOnly` from `DialOptions`; apply the effective mode in the connect path and after a member connects. Verify session tests for a mixed session and for a member added under the lock.
- [ ] 3.2 Add `Member.Mode()`, `Member.ReadOnly()` and `Session.SetReadOnly(id, bool)`; refuse the change under the lock and while the member has running jobs, checked under the job manager's lock. Verify tests for both refusals and for a successful toggle changing what the member's engine refuses.
- [ ] 3.3 Include each member's effective mode in what the UI uses for redraw and update existing session/attention tests that assumed one flag. Verify `go test ./internal/session`.

## 4. UI gates, display and picker toggle

- [ ] 4.1 Replace the cached `Model.readOnly` with `readOnlyLock`, `m.readOnly()` for the focused member and `m.readOnlyFor(mem)` for a named one; mechanically update every gate, help text, form and status message. Verify `go test ./internal/ui` compiles and the existing read-only tests pass after adjusting only their setup.
- [ ] 4.2 Show the focused member's effective mode in the header beside the link tag (never dropped at 80×24) and mark each read-only member in the strip; include the mode in the session signature used for redraw. Verify layout/responsive tests and a mixed-mode strip test.
- [ ] 4.3 In the picker, show each member's mode in words, explain the `--read-only` lock, and bind `o` to toggle the selected member; report refusals for the lock and for running jobs, and report a successful toggle in the status line. Verify picker tests for toggle, lock refusal, job refusal, and that nothing is sent to either daemon.
- [ ] 4.4 Gate the image copy on its destination: a read-only target refuses before the chooser or before the job, while a read-only source still allows a copy to a read-write target; keep the session lock overriding both. Verify both directions in `internal/ui` and `internal/plan` tests.
- [ ] 4.5 Update the in-app help for per-member modes, the `o` key and the saved-theme wording. Verify the help overlay test sees the new lines and `go test ./internal/ui -run 'ReadOnly|Help|Daemon'` passes.

## 5. Launch, theme precedence and restore input

- [ ] 5.1 Add `--no-state` to the flag set, usage and environment list; verify parsing tests and that `--help` shows it.
- [ ] 5.2 Load state before resolving the endpoint and choose the theme by `--theme`, then `DOCKER_DESK_THEME`, then a valid stored name, then `auto`; pass the requested name to the UI so `auto` stays automatic, and ignore an unknown stored name with one notice. Verify `cmd/docker-desk` tests for each precedence and for the unknown name.
- [ ] 5.3 Apply a stored mode to the launch member unless `--read-only` is in force, build the launch engine in that effective mode, and include it in `--check`'s `mode` line. Verify `--check` tests for stored read-only, stored read-write, and `--read-only` overriding both.
- [ ] 5.4 Resolve restored entries into endpoints: a non-`default` context is authoritative when usable; a direct `unix://` or `ssh://` endpoint is validated; a `tcp://` endpoint without a resolvable context is skipped. Collect one notice per skipped entry. Verify tests with a temporary Docker configuration directory.
- [ ] 5.5 Pass restored members, the stored focus key and startup notices to `ui.Options`; do not read or write state in `--report`, and do not add restored members in `--check`. Verify main tests for both headless paths and `--no-state`.

## 6. Restoring the session in the UI

- [ ] 6.1 Add restored members after the launch member with their stored modes, keep the stored focus key pending, and move focus with a status notice the first time that member connects. Verify a UI test with two fake daemons where the second answers later.
- [ ] 6.2 Cancel the pending stored focus on any explicit focus change (picker, `>`/`<`, Attention jump), and keep focus on the launch daemon when the stored member never connects. Verify tests for both.
- [ ] 6.3 Build the save snapshot from the theme choice, focused member's key and every member's endpoint, context and intended mode; save after theme changes, add, remove, actual focus change and mode toggle, and report a save failure once without interrupting the desk. Verify a UI test with a temporary store and a failing store.
- [ ] 6.4 Confirm `--no-state` leaves no file and no save call in the UI; verify with a temporary XDG directory and the real binary.

## 7. Integration and documentation

- [ ] 7.1 Add an end-to-end test with a temporary XDG directory: first run adds a second daemon, toggles it read-only, switches theme and focuses it; second run restores all four with no `M`, and a third with `--no-state` starts empty. Verify `go test ./internal/e2e -run State`.
- [ ] 7.2 Add an end-to-end test for a corrupt file and a `0666` file: the desk opens, says once that the file was ignored, sends nothing on that account, and `--read-only` overrides a stored read-write mode. Verify the test observes only reads.
- [ ] 7.3 Update README (flags, themes, several-daemons strip, a new state-file section with location, permissions, contents and `--no-state`) and refresh the affected `docs/screens/` captures. Verify the commands and paths written there by running `--help` and `--check` against the fake daemon.
- [ ] 7.4 Add the design decisions to `docs/DECISIONS.md` and the coverage rows to `docs/TESTING.md`, including the trust boundary and the two-layer toggle tests. Verify no referenced test name is missing from the suite.
- [ ] 7.5 Run the full checks: `go vet ./...`, `go test ./...`, and `go test -race ./internal/engine ./internal/session ./internal/ui`. Verify all pass, with the real-daemon checks untouched.

## Workflow follow-up

- Archive the change after implementation and review are complete.
- Verify the archived spec deltas merged into the main specs and that the pre-existing theme rule ("the choice SHALL NOT be stored") is gone.
