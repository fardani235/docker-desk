# Verification — multi-daemon-session

What was run to check this change, on this machine (Linux, Go 1.26, Docker CLI 29.9.3, OpenSSH
client and server installed), and what came of it. Nothing here touched a real daemon: every
daemon was `internal/fakeengine`, and the test guard (B3) was on in every package.

## Build and static checks

| Command | Result |
| --- | --- |
| `go build ./...` | clean |
| `go vet ./...` | clean |
| `gofmt -l ./internal ./cmd` | clean |
| `openspec validate multi-daemon-session` | valid |

## Task 2.2 — the picker's lister against `docker context ls`

`dockerctx.TestListContextsMatchesDockerCLI` builds a configuration directory holding the contexts
`good` (`unix://`), `pipe` (`npipe://`), `prod` (`ssh://ops@prod-1:2222`) and `tls`
(`tcp://10.0.0.5:2376`, `SkipTLSVerify`), with `currentContext: good`. It runs the real
`docker context ls --format json` on that directory — with only `PATH`, `HOME` and
`DOCKER_CONFIG` in its environment — under three settings: nothing else, `DOCKER_HOST` set, and
`DOCKER_CONTEXT=prod`. Each time, name, endpoint, description and current marker agree entry by
entry, in the same order. **Passed.** The CLI reads the store only; no daemon is contacted.

Checked by hand first, to learn what the CLI does with what the test does not cover:

- It sorts by name with `default` among the others (`aaa`, `default`, `good`, …).
- It describes the implicit context as `Current DOCKER_HOST based configuration`, and gives it
  `DOCKER_HOST`'s endpoint when that is set.
- A malformed `meta.json` makes it list nothing at all (`failed to read metadata: … unexpected
  end of JSON input`, exit 1). The picker lists that entry as unusable instead (M20), and
  `TestListContextsUnreadableEntries` covers it.

## Task 8.1 — end-to-end, two daemons, one over ssh

`go test ./internal/e2e/ -count=1 -run 'TestTwoDaemonsOneOverSSH|TestRemovingTheLaunchDaemonOverSSH'`
(real binary, pseudo-terminal, real `ssh` and `sshd`): **passed**, uncached. It covers adding over
ssh without a question, focus both ways, a pull on the ssh daemon, the removal refused while the
pull runs and naming it, the background exit as a notice (not an Events line), the pull's outcome
named by daemon, removal with its ssh process gone within the second, quit with no ssh and no
tunnel directory left, and removal of the launch daemon (over ssh) down to the empty state and
back. Its three screens are `docs/screens/51-second-daemon-focused.txt`,
`52-remove-refused-while-pulling.txt` and `53-no-daemon.txt` (written with `-update-screens` for
these two tests only; no other screen was rewritten).

## Task 8.3 — nothing left behind

- `session.TestNoGoroutineOrSSHLeftBehind`: four members (the launch one, a second socket, one over
  an ssh stub, one that never answers), streams open on two of them, a focus change; removing three
  frees goroutines and ends the stub ssh; `Close` (what quitting does) brings the goroutine count
  back to what it was before the session existed, and no ssh process remains. **Passed.**
- `session.TestPickedSSHNeverAsks` and `TestPickedSSHThatConnects`: after removal no further ssh
  is started, and the one that ran has exited. **Passed.**
- The two e2e scenarios above count the ssh processes that use the test server's configuration
  after each removal and after quit: none. **Passed.**

## Task 8.2 — the whole suite, and the race detector

| Run | Result |
| --- | --- |
| `go test -count=1 ./...` | **all 25 packages passed** |
| `go test -race -count=1 ./...`, first | no data race; failed `e2e.TestSearchInTheSandbox` and `e2e.TestRepositoryBrowser` (each waited for a line now one row below the screen — the strip's row, M11; their terminals are one row taller now) and `ui.TestSearchBarCostsNoRowWhenClosed` (below) |
| `go test -race -count=1 ./internal/e2e/ ./internal/ui/` | no data race; `e2e` passed; `ui` failed only `TestSearchBarCostsNoRowWhenClosed` |
| `TestSearchBarCostsNoRowWhenClosed` under `-race`, 20 runs each | `HEAD` (clean worktree): 8 failed; this tree: 16 failed. The two screenshots differed by a live CPU figure only. The test now holds the fake's statistics still: **20 of 20 passed** (M22) |
| `go test -race -count=1 ./...`, second | no data race; 24 of 25 packages passed; `live.TestHubLogBufferIsBounded` timed out filling 15,000 log lines in 5 s under the full parallel load. Alone under `-race`: 10 of 10 passed, in 1.9–2.8 s. The change adds nothing per log line (one mode check when a stream opens) |
| `go test -race -count=1 ./...`, third, on the final code | **all 25 packages passed**, no data race |

No run reported a data race.

## Not run

- `make crosscheck` drives the fake with the real CLI's container, image and system commands; this
  change does not touch the fake beyond a counter of open statistics streams, and B34 records that
  script's three pre-existing failures.
- `make real-readonly-check` reads a real daemon; nothing in the engine layer changed.
- `make e2e` in full with `-update-screens`: it rewrites every screen under `docs/screens` (B34);
  only this change's three were written.
