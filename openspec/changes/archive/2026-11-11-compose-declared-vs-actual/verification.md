# Verification record

What was run while applying the change, and what was not. Decisions taken where the plan left a
choice open are recorded in `docs/DECISIONS.md`, Y10–Y15.

## Passed

- `go list -m go.yaml.in/yaml/v3` → `go.yaml.in/yaml/v3 v3.0.6`; `go build ./...` passes.
- `make test`, `make vet`, `make race`: pass. (Two timing-sensitive tests outside this change,
  `internal/live` `TestHubLogBufferIsBounded` and `internal/ui`
  `TestSearchBarCostsNoRowWhenClosed`, failed once each under load — the second because a live
  CPU figure moved from 49.2% to 49.3% between its two screenshots — and pass on rerun, alone
  and in the full race run.)
- `FuzzParse` for 30 s (177,591 executions) without a failure. The `recover` net was removed
  by hand once: `TestParseNetCatchesWhatParseDoesNot` then failed with the planted panic, as
  task 1.4 asks.
- `make e2e`: `TestComposeDeclared` drives the real binary on a pseudo-terminal against the fake
  daemon with its compose fixtures; screens `docs/screens/44-compose-declared.txt`,
  `45-compose-declared-80x24-read-only.txt` and `46-compose-declared-absent.txt` were written.
  The other screens changed only in their random socket paths and live figures, and in the
  Compose Info sentence and tab strip.
- `bin/fake-dockerd -compose-dir DIR` writes `DIR/shop/compose.yaml` (write time 20 minutes
  back), leaves `DIR/blog/compose.yaml` absent, and the seeded labels point at both. (`make
  sandbox` itself needs an interactive terminal and was not opened here; the same code path is
  the e2e session above.)
- 6.3, offline half: the only `open` in `internal/composefile` is
  `os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)`; nothing there creates, writes,
  renames or removes. `internal/ui/declared.go` makes no daemon request (it reads the snapshot
  the lists already hold). The UI test `TestDeclaredComparisonShowsEachVerdict` runs in
  read-only mode and asserts no mutating request reached the fake.

## Not run

- **6.2 — the real daemon.** Launching `docker-desk --read-only` against
  `unix:///var/run/docker.sock` (in a detached tmux session, to drive `gitlab` › Compose ›
  Declared) was denied in this session, and no other route to the real daemon was tried. What
  can be said without it: the real `/home/ridwan/workspace/gitlab/compose.yaml` has the shape of
  the `internal/composefile/testdata/gitlab.yaml` fixture (template image, 20-line block scalar
  with a flow map), which parses to one service, `gitlab`, whose image is reported as a
  template and not compared. That the service set matches the running container, and that the
  file was written four seconds before the container's creation, is still to be checked by
  running the desk on that machine.
- **6.3 — `make real-readonly-check`.** It talks to the real daemon and was not run, for the
  same reason. Task 6.3 stays open until it is.
