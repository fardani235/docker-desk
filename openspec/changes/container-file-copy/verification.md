# Verification — container-file-copy

What was run to check this change, on this machine (Linux, Go 1.26.5, Docker CLI and Engine
29.9.3), and what came of it. Every mutating request went to `internal/fakeengine`; the test guard
(B3) was on in every package. The one real-daemon test is read-only and was run by hand on one
container, on two harmless paths.

## Build and static checks

| Command | Result |
| --- | --- |
| `make build` | clean |
| `make vet` | clean |
| `gofmt -l ./cmd ./internal` | clean |
| `openspec validate container-file-copy` | valid |

## Tests

| Command | Result |
| --- | --- |
| `go test ./... -count=1` | **passed**, 26 packages |
| `go test -race` on `transfer`, `plan`, `fakeengine`, `engine`, `files` and the new UI tests | passed |
| `go test ./internal/e2e -run Copy -count=1`, three times in a row | passed 3/3 |
| `go test ./internal/engine -run TestRealDaemonCopyReadOnly` without `DOCKER_DESK_REAL_*` | skipped, with its instruction |

Every command in `docs/TESTING.md`'s round-eighteen table was run and selects at least one
passing test.

## `make crosscheck` (task 2.6)

99 passed, 3 failed. The `cp` section's 10 checks all pass against the real `docker cp`. The three
failures — `dangling images`, `system df images`, `system df containers` — **predate this
change**: the script on the unchanged `HEAD`, in a clean worktree, gives 89 passed and the same 3
failed. They were not changed here (B34, B35 record them too).

## The real daemon, read-only (task 6.3, the half that was done)

```
DOCKER_DESK_REAL_HOST=unix:///var/run/docker.sock \
DOCKER_DESK_REAL_COPY_CONTAINER=gitsearch-platform-frontend-1 DOCKER_DESK_REAL_COPY_PATH=<path> \
go test ./internal/engine/ -run TestRealDaemonCopyReadOnly -count=1 -v
```

- `/etc/os-release` — a symlink: one link, recreated as a link. **Passed.**
- `/etc/ssl` — a directory: 5 files, 3 directories, 1 link, 243,415 bytes. **Passed.**

Each time the desk's copy (read-only engine, `CopyFromContainer`, `transfer.Extract`) and the real
`docker cp`'s copy of the same path were identical in names, types, permission bits, link
targets and bytes (SHA-256), and `CopyToContainer` through the read-only engine was refused with
`ErrReadOnly` before a request.

**Not done:** the "back in" half of task 6.3. A copy in is a `PUT`; the suite's test guard
refuses any test a read-write connection to a real daemon, and "guarded like the other real-daemon
tests" means read-only. The guard was not loosened. Task 6.3 is left unchecked for the owner to
decide (docs/DECISIONS.md B36).

## Decisions taken while implementing

Recorded in `docs/DECISIONS.md`, round eighteen, W11–W22. The ones that correct the design:

- W11 — this is round eighteen; round seventeen was creating a container.
- W12 — Docker does emit container events for a copy (`archive-path`, `extract-to-dir`); the
  job watch of W7 is still what makes the view follow a copy in.
- W13 — a directory's mode is applied after its contents, not before.
- W17 — the demo's `state.db` is at `/var/lib/nginx/state.db`, where its change list puts it.
