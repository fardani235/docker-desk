# Tasks

## 1. Session core

- [ ] 1.1 Add `internal/session` with an ordered member list, one focused member, and add, remove and focus operations; verify with unit tests that focus never points at a missing member and that removing the focused member moves focus to a connected one
- [ ] 1.2 Add duplicate detection on the canonical endpoint (scheme, host, port, socket path, TLS identity); verify with a test that two context names resolving to one daemon yield one member
- [ ] 1.3 Add the removing state and the job check used by removal; verify with a test that a member with a running job is refused and the refusal names the job (depends on 4.2)

## 2. Contexts for the picker

- [ ] 2.1 Add a lister in `internal/dockerctx` that returns every stored context with its resolved endpoint or the reason it does not resolve; verify with a test over a temporary Docker configuration directory holding one good, one unusable and one `ssh://` context
- [ ] 2.2 Verify the lister's output matches `docker context ls` for the same directory, using the Docker CLI on a fixture (the existing real-CLI harness, if present, or a documented manual check recorded in `verification.md`)

## 3. Hub modes and focus change

- [ ] 3.1 Add an events-only mode to `live.Hub` (event stream and ping only) and a full mode; verify with a test that an events-only hub opens no statistics or log streams
- [ ] 3.2 Implement focus change: stop the previous member's statistics and log streams, clear its list caches from the panes' view, and request the new member's lists; verify with two fake daemons that the first daemon's objects never appear after focus moves
- [ ] 3.3 Keep one event ring per member so focusing a member shows the events recorded while unfocused; verify with a test that a background event appears only after focusing its member

## 4. Jobs

- [ ] 4.1 Add a `Daemon` field to `jobs.Job` and record it when a job starts; verify with a unit test that the job reports the daemon it was started on
- [ ] 4.2 Make job start and member removal check the same lock, so a job cannot start on a member marked removing; verify with a concurrent test that starts a job and removes its member and asserts exactly one of the two wins

## 5. Connecting members

- [ ] 5.1 Add the picker path that builds an engine, pings it within the time bound and returns a connected or disconnected member, without exiting; verify with a test that an unreachable endpoint yields a disconnected member and the session stays usable
- [ ] 5.2 Run ssh for picker-added members in batch mode so no passphrase, password or host-key question is asked; verify with a test using the ssh stub that a key needing a passphrase yields a disconnected member with ssh's reason and writes nothing to the terminal
- [ ] 5.3 Apply the session's single read-only flag to every engine it creates, including members added later; verify with a test that a member added in a read-only session refuses a mutation before any request is sent

## 6. Workspace

- [ ] 6.1 Show the focused member's identity in the header and the daemon strip in every layout; verify with the layout tests at 80x24 and 120x30 with three members, including the focused marker and the disconnected state
- [ ] 6.2 Add the focus key and the strip navigation to the key switch; verify the chosen keys do not conflict with `internal/ui/keys.go` by a test that presses each key in each pane
- [ ] 6.3 Add the picker dialog listing contexts, with the endpoint, the transport tag and the remote tag, and a confirmation before a member is removed; verify with dialog tests at 80x24 that the endpoint is shown in full
- [ ] 6.4 Add the empty state shown when no member is connected, with the picker one key away and quit still working; verify with a test that an action key in the empty state changes nothing and says no daemon is connected
- [ ] 6.5 Route background notices to the status line for container exits and member disconnect and reconnect; verify with a test that an exit on an unfocused member appears as one notice naming the member and not in the focused Events pane

## 7. Documentation

- [ ] 7.1 Update the README and the operator guide to describe the picker, the focus key, background notices and the read-only session rule; verify each documented key against the key switch in `internal/ui/keys.go`
- [ ] 7.2 Record in docs/DECISIONS.md that B20's and R7's single-daemon reasoning is superseded by this change, with the reasons for D1 to D9; verify the entries exist and link to the change

## 8. Integration checks

- [ ] 8.1 Run an end-to-end scenario with two fake daemons, one of them ssh: add, focus, remove, notices, and a refused removal during a pull; verify it passes uncached under `go test`
- [ ] 8.2 Run the race detector and the full test suite; verify both are clean
- [ ] 8.3 Check for leaked goroutines and ssh processes after removing members and after quit; verify the counts return to the baseline

## Workflow follow-up

- Archive the change after review, then run `openspec validate --specs` to confirm the main specs are coherent.
