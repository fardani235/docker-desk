# How Docker Desk is proven — without touching your containers

The constraint was: prove that a tool which stops, kills and deletes things works, **without
ever mutating the containers that actually exist on this machine**. This file says how that
was done, what was measured, how to repeat it, and — just as important — what is *not* proven.

## The short version

| What | With | Real daemon involved? |
| --- | --- | --- |
| Every rule about what may be changed, and how | pure planner tests | no |
| Every request the desk can send | Docker's own client library against a fake daemon | no |
| The fake daemon itself | the real `docker` CLI driving it, 70 assertions | no |
| Every key, pane and dialog | the UI model driven key by key; the fake's request log is the witness | no |
| Responsiveness under a log flood | model-level and real-binary measurements | no |
| The real program, end to end, including the shell hand-off | the binary on a pseudo-terminal with a terminal emulator | no |
| That it reads a real engine correctly | the engine layer, then the whole program, in read-only mode | **yes — `GET`/`HEAD` only, enforced** |
| That nothing on the real engine changed | before/after comparison of every container, image, volume and network | read-only |
| Remote daemons over TCP and TLS (round two) | the client library and the real binary against the fake daemon on a loopback port, with real certificates | no |
| Remote daemons over SSH (round two) | the real `ssh` client through a real `sshd` on a loopback port to the fake daemon's socket | no |
| The Docker context store and TLS rules (round two) | a context written and used by the real `docker` CLI, then read by the desk | no |
| Themes (round two) | every frame compared across themes; contrast computed; the real binary's colour output | no |
| Container processes (round six) | engine and fake-daemon tests of `/top`; UI lifecycle tests counting `/top` requests; the real binary on a pseudo-terminal | no |
| Disk accounting (round five) | pure accounting tests; the real binary's totals against the real `docker system df` on the fake | opt-in, read-only: once |

`go test ./...` runs 399 tests and subtests — 198 tests, the rest their sub-cases. (Round one
ended at 194 in all.) Two are opt-in and skipped by default. Twelve tests need a real `sshd`
and skip themselves on a machine without OpenSSH's server; on this one none was skipped, and
`DOCKER_DESK_REQUIRE_SSHD=1` turns such a skip into a failure, so that a helper that quietly
stopped working cannot hide behind a green run. Everything passes with and without the race
detector.

The remote transports and the themes have [their own section](#round-two-remote-daemons-and-themes)
below; so do [image findings](#round-four-image-findings) and [Space](#round-five-space). At
the end of round five, `go test ./...` passes 486 tests and subtests, with four opt-in
real-daemon tests skipped.

## Why a fake daemon, and why it can be trusted

`internal/fakeengine` is an in-memory Docker Engine that serves the real Engine HTTP API on a
unix socket. The desk talks to it through the same client library, the same code path, as it
talks to a real daemon — so request encoding, response decoding, the multiplexed log framing
and the hijacked exec connection are all exercised for real.

A fake written alongside the code it tests can agree with that code and still be wrong. It is
therefore checked from both directions:

1. **Docker's client against the fake.** `make crosscheck` runs the real `docker` CLI against
   the fake and asserts on the results: `ps`, `inspect`, `stop`/`start`/`pause`/`kill`/`rm`,
   `events`, `logs -f`, `stats`, `exec`, `exec -it` on a pty, `pull`, `rmi`, volumes,
   networks — including the refusals ("container is running", "volume is in use", "has active
   endpoints", "pre-defined network"). 61 assertions.

   The script cannot reach the real daemon: every `docker` invocation carries an explicit `-H`
   to a socket in a private temporary directory, `DOCKER_HOST`/`DOCKER_CONTEXT` are unset
   first, and it aborts before any mutation unless the server on that socket identifies itself
   as the fake.

   The first run of this script found a real protocol error in the fake (how a process that
   cannot start is reported — see the journal, §4). That is the point of having it.

2. **The desk against Docker's server**, read-only (below).

And the request *encoding* is not ours at all: it is the Moby client's, the one the CLI uses.

## Nothing in the test suite can mutate a real daemon

This is enforced, not promised:

- Every test package that builds an engine calls `engine.InstallTestGuard()` in `TestMain`.
  After that, `engine.New` refuses a read-write connection to any socket outside the system
  temporary directory (`TestGuardRefusesRealSockets`), and `DOCKER_HOST` is pointed at a path
  that cannot exist.
- The same guard covers the ways in that round two added. A TCP address may be opened
  read-write only if the fake engine itself is listening on it — it registers the address when
  it opens the listener (`TestGuardAllowsOnlyTheFakesOwnListeners`). An SSH tunnel may be
  opened only to a remote socket under the temporary directory: the tunnel's *local* end is
  always there, so without that check a tunnel would have been a way around the first rule
  (`TestGuardRefusesTunnelsToRealSockets`).
- The two tests that do visit a real daemon are opt-in (`DOCKER_DESK_REAL_HOST=…`) and open it
  with `ReadOnly: true`. In read-only mode a hook on the HTTP client rejects every request
  that is not `GET` or `HEAD` before it is written to the socket. The test proves the hook
  first, by aiming a `DELETE` at a name that cannot exist.
- The end-to-end harness starts the binary with a minimal environment; your `DOCKER_*`
  variables are never inherited. Since round two that environment also names a daemon that
  cannot exist and a Docker configuration directory that does not: a launch that forgot to say
  which daemon it wants fails, instead of finding yours through your Docker context.

## The safety properties, as tests

All in `internal/ui/safety_test.go` unless noted. The witness in each case is the fake
daemon's request log — what actually arrived — not the UI's opinion of what it did.

- **`TestNoSingleKeystrokeMutates`** — for each of the 18 confirm-tier and typed-tier actions
  (start, stop, restart, pause, unpause, kill, remove container, remove image, untag image,
  remove volume, remove network, connect, disconnect, stop project, remove project, prune
  images/volumes/networks):
  1. the action key alone sends nothing and opens a dialog showing the title, the object, the
     effect and the daemon endpoint;
  2. Esc sends nothing and leaves no job;
  3. for `y` confirmations, Enter, space, `Y`, other letters and Tab send nothing; for typed
     confirmations, Enter on empty input and a mistyped word send nothing and the dialog says
     so;
  4. the deliberate input sends exactly the announced request(s), none carrying `force`.
- **`TestEveryActionKeyIsCovered`** — an action added to the desk without a row in that table,
  or not marked as mutating, fails the build's tests.
- **`TestBlockedDeletionsCannotBeConfirmed`** — ten in-use cases (running and paused
  containers; images used by running and by stopped containers; volumes; networks, including
  one referenced only by stopped containers; a predefined network; a running compose project):
  the dialog says what uses the object, offers no input, and no key proceeds.
- **`TestConfirmationIsBoundToTheObjectItShowed`** — the list is reshuffled under an open
  confirmation and the highlight is even moved elsewhere; confirming acts on the object the
  dialog named.
- **`TestPruneShowsTheExactSetAndDestroysOnlyThat`** and, at the planner level,
  **`TestPruneDeletesExactlyTheListedSet`** — a volume created after the preview survives the
  prune; exactly one `DELETE` per listed object is sent; nothing resembling a prune endpoint.
- **`TestListedObjectThatBecameInUseIsRefusedAndTheRestContinue`** (`internal/plan`) — the
  daemon's refusal is recorded for that object, it is not deleted, the others are.
- **`TestNetworkPruneKeepsNetworksOfStoppedContainers`** — the conservative in-use rule.
- **`TestProjectRemoveTouchesOnlyItsContainers`** — only container `DELETE`s; networks and
  volumes counted before and after.
- **`TestReadOnlyDisablesEveryAction`**, **`TestReadOnlySendsNothing`**,
  **`TestReadOnlyCoversEveryMutation`** (`internal/engine`, by reflection over the interface),
  and **`TestReadOnlyFlag`** (the real binary: every request it sent was `GET`/`HEAD`).
- **`TestNeverForces`** (`internal/engine`) — calls every mutating method and reads the
  queries that went over the wire.

- **`TestAnswersToAbandonedRequestsDoNothing`** — the gaps between asking the daemon and
  getting the answer: a form cancelled while its check was in flight is not submitted; a prune
  preview being recomputed cannot be confirmed; a late confirmation does not replace another
  overlay; and keys typed for something else — a filter word containing `y`, Enter in a
  picker, `t` followed at once by `y` — cannot confirm a dialog that opens late.
- **`TestConfirmationsMustBeReadFirst`** — within the arming delay `y`, the typed word and
  Enter on the pre-filled shell form are ignored (and early text is not kept); afterwards they
  work; Esc is never delayed.
- **`TestNothingActsWhileTheDeskCannotBeDrawn`** — in a too-small terminal sixteen keys send
  no request at all and change no state; a dialog open when the window shrinks cannot be
  confirmed and is back, unconfirmed, when it grows.
- **`TestActionMenuActsOnTheObjectItNames`** — the highlight is moved, and the object removed,
  under an open menu.
- In `internal/plan`: **`TestReferenceMovedAfterConfirmationIsRefused`**,
  **`TestUntagThatWouldNowDeleteIsRefused`**,
  **`TestPruneDoesNotUntagAnImageThatBecameInUse`**,
  **`TestVolumeRecreatedAfterConfirmationIsRefused`** — the name-pinning checks, each asserting
  that zero mutating requests were sent.

**Do these tests have teeth?** They were checked by breaking the code on purpose. Three of the
review fixes were taken out one at a time: without "a key drops the pending action" the filter
test fails with "the confirmation opened under the filter"; without the too-small guard the
fake's log shows a real `POST …/stop`; with the menu re-reading the highlight the plan targets
the wrong container. And for the original rules: Making Enter
count as consent fails `TestNoSingleKeystrokeMutates/stop` with "1 mutating request(s) reached
the daemon"; accepting a prefix of the confirmation word fails `…/remove_volume` the same way.
(Both changes were reverted.)

## The independent review

After the suite was green, the code between a key press and a mutating request was given to a
separate reviewer, with the rules it must obey and nothing about why it was believed to obey
them, and asked for concrete sequences that break a rule. It found eight things worth fixing —
the most serious being a confirmation that could be confirmed by keys typed for something
else, and dialogs that stayed live in a terminal too small to draw them. All are fixed and
have tests; the list, with what was kept as it was and why, is in
[DECISIONS.md](DECISIONS.md#b30--an-independent-review-of-the-mutation-path-before-calling-it-done).

It also recorded what it examined and found sound: Enter never confirms a `y` dialog; the
typed tier needs an exact match; blockers are enforced three times over; no force option is
set anywhere; no prune endpoint is called; project removal emits only container removals;
read-only mode covers every mutation and exec.

The review read code; it ran nothing against any daemon.

## Responsiveness: measurements

All figures are from this machine (4 cores, Linux), with the fake daemon in the same process
or on the same host. They are evidence that the bounds hold by design, not benchmarks to
compare across machines.

**Model level** — `TestFloodDoesNotStarveTheDesk` (`internal/ui/responsive_test.go`). One
container floods its log while nine statistics streams and the event stream run, at the
production frame interval of 50 ms.

| Measured | Result | Bound asserted |
| --- | --- | --- |
| Log lines reaching the desk | ≈ 300,000 per second | ≥ 50,000 for the test to mean anything |
| Frames rendered in 1.5 s | 29 | ≤ 1.5 s ÷ 50 ms + 3 — the frame rate, not the line rate |
| Messages through the UI queue in 1.5 s | 29 | ≤ frame ticks + 30 — i.e. no stream data in the queue |
| Key → new state + full redraw | p50 0.7 ms, p95 1.5 ms, max 2–4 ms | < 100 ms |
| Log buffer after the flood | exactly 5,000 lines | = 5,000 |
| Statistics history per container | ≤ 120 samples | ≤ 120 |

Related: `BenchmarkFrame` — one full redraw at 180×50 while ingesting 200,000 lines/s costs
about 0.66 ms. `BenchmarkLineWriter` — splitting and sanitising log output runs at about
3 million lines per second. `TestEventBurstCausesBoundedRefreshes` — 101 events cause one or
two list requests. `TestIdleDeskDoesNotRedraw` — with nothing streaming, 50+ frame ticks render
zero frames. `TestLongLinesAreBounded` — a 3 MiB line with no newline is split into 2 KiB
pieces.

**Real binary through a real pseudo-terminal** — `TestResponsiveUnderFlood`
(`internal/e2e`). The program runs as a separate process; Tab is pressed 40 times and the time
until the emulated screen shows the focus on the next pane is measured.

| Measured | Result | Bound asserted |
| --- | --- | --- |
| Lines written by the container | ≈ 199,000 per second | — |
| Lines ingested by the desk (from its own counter) | ≈ 196,000 per second | ≥ 50,000 |
| Key → visible on screen | p50 ≈ 12 ms, p95 ≈ 26 ms, max ≈ 27 ms | p95 < 100 ms |

Under the race detector these two timing figures are logged rather than asserted. In the
model-level test, stream ingestion itself runs an order of magnitude slower, so the "≥ 50,000
lines/s" precondition can fail; the frame, queue and buffer bounds are still asserted. In the
real-binary test the program is built without the detector, but the harness measuring it — the
fake daemon generating the flood, the terminal emulator, the polling loop — is slowed several
times over while competing with the other test binaries for four cores, and the latency it
reports (p95 around 100 ms in such a run) is no longer the desk's.

## End to end: the real program on a pseudo-terminal

`internal/ptyscreen` runs a program on a pty and keeps an emulated screen of what it draws;
`internal/e2e` uses it to drive the real `docker-desk` binary the way a person does. (Terminal
multiplexers were not available to the build session; this harness is what replaced them.)

`TestSession` is one continuous sitting, asserting on the screen and on the fake's request log
at each step: the five panes and the endpoint → moving through the kinds → filtering to a
container → `t` opens a confirmation and sends nothing → Enter is not consent → `y` → the
engine's events appear in the Events pane and the job in the Jobs pane → logs of the stopped
container → start it and follow → statistics → inspect → pull an image as a background job
while navigating elsewhere → prune with a preview, a scope toggle, a mistyped word and then the
real one → a refused removal → **the shell**: the terminal is handed over (the desk leaves the
alternate screen), keys reach the process, the container reports the terminal's real size, and
on `exit` the desk is back with the same filter, highlight and focus → help → quit, with the
alternate screen left.

Screens captured during that session are in [`docs/screens/`](screens/) — plain text, exactly
what the emulator showed. `make e2e` regenerates them.

Other end-to-end tests: the 80×24 layout and resizing down to "too small" and back; `NO_COLOR`
(no colour sequence in the entire output, focus still marked); `--read-only`; the daemon
stopping and restarting under a running desk; refusing to open without a daemon, and with a
remote endpoint (the alternate-screen sequence never appears in the output); quitting with a
job running; and `make sandbox` as documented.

The shell's terminal handling has its own pseudo-terminal tests (`internal/shell`): raw mode
during the session, the exact terminal modes restored afterwards (compared field by field),
resize propagation, Ctrl-C delivered to the remote process, the container dying mid-session,
the daemon going away mid-session, a shell that cannot start, a daemon that never starts the
process (bounded wait), the local escape (Ctrl-] three times), `SIGTERM` ending the session
cleanly, terminal modes switched off afterwards, and — the subtle one — that no reader is left
behind to swallow the first key typed back at the desk.

## The real daemon: looked at, never touched

Two opt-in checks talk to the real engine, both read-only:

```sh
make real-readonly-check     # the engine layer: list, inspect, logs tail, stats, events
DOCKER_DESK_REAL_HOST=unix:///var/run/docker.sock \
  go test ./internal/e2e/ -run TestRealDaemonReadOnlyTour -count=1 -v -show-screens
```

The first found three things about real data that changed the code (journal, §3). The second
runs the real binary with `--read-only` against the real daemon and visits every kind, every
Details tab, the Events and Jobs panes and the help, pressing every mutating key on real
objects along the way — each is answered with "Read-only mode… Nothing was sent to the
daemon". On this machine it showed the two real containers with live CPU and memory for the
running one, three images, one volume and four networks; the `minikube` network was shown with
one container although its only container is stopped, which is the conservative rule at work.

**Before and after.** A snapshot of the real daemon was recorded before the first line of code
was written and again after all of the above:

| Compared | Fields | Entries | Result |
| --- | --- | --- | --- |
| Containers | full ID, name, image, state, creation time | 2 | identical |
| Container state | full ID, status, started-at, finished-at, restart count | 2 | identical |
| Images | full ID, repository:tag (including intermediate) | 3 | identical |
| Volumes | name, driver | 1 | identical |
| Networks | full ID, name, driver | 4 | identical |

The running container's started-at is unchanged, its restart count is unchanged, and it has no
exec sessions. To repeat it on your own machine, record the output of these before and after
and `diff` them:

```sh
docker ps -a --no-trunc --format '{{.ID}} {{.Names}} {{.Image}} {{.State}} {{.CreatedAt}}' | sort
docker inspect --format '{{.Id}} {{.State.Status}} {{.State.StartedAt}} {{.State.FinishedAt}} {{.RestartCount}}' $(docker ps -aq) | sort
docker images -a --no-trunc --format '{{.ID}} {{.Repository}}:{{.Tag}}' | sort
docker volume ls --format '{{.Name}} {{.Driver}}' | sort
docker network ls --no-trunc --format '{{.ID}} {{.Name}} {{.Driver}}' | sort
```

## Round two: remote daemons and themes

### The principle: real protocols, fake daemon

The daemon at the far end is always the fake. Everything *between* the desk and it is real
wherever a real implementation exists on the machine:

| Between the desk and the fake daemon | Is it the real thing? |
| --- | --- |
| TCP | yes — a loopback socket |
| TLS | yes — Go's TLS on both ends, a throwaway CA, a server certificate, a client certificate the server requires |
| The ssh client | **yes — `/usr/bin/ssh`**, started by the desk with the command line it uses in production |
| The SSH server | **yes — `/usr/sbin/sshd`**, unprivileged, on a loopback port, with its own host key and one throwaway client key (`internal/sshtest`) |
| The Docker context store | **written by the real `docker` CLI** (`docker context create … --docker host=…,ca=…,cert=…,key=…`) |
| The network | no — everything is on one machine |
| The remote Docker daemon | no — the fake, as in round one |

The throwaway `sshd` accepts public-key logins with the throwaway key only, for the account
running the tests, with no terminal and no agent or X11 forwarding; the client is run with a
configuration file of its own (`-F`), so neither `~/.ssh/config`, nor the agent, nor any real
key or `known_hosts` is read or written. It lives for the duration of a test. Tests that need
it skip themselves where OpenSSH's server is not installed.

### What is proven, by layer

**Resolution and parsing** (`internal/dockerctx`). `TestResolve` — 20 cases of precedence:
both flags, `--context default` not getting past `DOCKER_HOST`, the CLI's
`tcp://localhost:2376` when TLS is asked for and nothing is named, no home directory, control
characters in an endpoint (from the environment and from a context file) refused rather than
printed. `TestResolveTLS` — 15 cases of where TLS settings come from, including "any non-empty
`DOCKER_TLS_VERIFY` counts", "a certificate without its key is left out", "verifying without a
CA is an error", "a context ignores the environment" and "half a key pair in a context is
still TLS, never plain TCP". `TestTarget` — 11 endpoints that parse and 28 that must not:
hosts and users `ssh` would read as an option, plainly and percent-encoded; socket paths with
`${VAR}`, `%` tokens, spaces and escape sequences; hosts with shell metacharacters.
`TestLink` — the tag for each kind of endpoint, and the port that will be dialled.
`TestClassifyTCP` (14 failures), `TestCheckTCP` — each named for what it is, with a hint that
fits where TLS was configured.

**The client library over TCP and TLS** (`internal/engine`). `TestOverTCPAndTLS` — requests, a
long-lived event stream, a mutation and the hijacked exec, over plain TCP and over mutual TLS.
`TestTLSIsActuallyEnforced` — without TLS, without a client certificate, trusting another CA,
or holding a certificate from another CA, the same port lets nothing in; so the passing cases
are not passing by accident. `TestTLSMaterialIsCheckedAtConstruction`.
`TestAttachIsBoundedOverTLS` — a peer that accepts and then says nothing cannot hold a shell's
start beyond its deadline. `TestErrorsNameTheDaemonNotTheTunnel`.

**The tunnel, against a stand-in ssh** (`internal/sshtunnel`). The test binary re-executes
itself as "ssh": it takes ssh's command line, forwards, and fails the ways ssh fails. This
makes every path of the supervisor run on any machine: the exact command line
(`TestCommandLine`), forwarding, ssh's own words on failure and no retry behind the operator's
back, a bounded wait, an immediate failure when the forwarding cannot be set up, interruption,
nothing left behind, reconnection, "keeps trying and says why", batch mode on every
reconnection, `Close` in every state. And, from the review: `TestCheckDestination` (5
destinations that may reach ssh, 20 that may not) and `TestUnsafeDestinationNeverReachesSSH`
(ssh is not started at all, and the same holds for a temporary directory ssh would expand);
`TestBatchRunsHaveNoTerminal` (a reconnection and the remote probe lead a session of their
own; the interactive first connection does not); `TestSSHThatPredatesAnOption`;
`TestSSHThatGoesToTheBackground`; `TestHintsFitTheFailure`.

**The tunnel, with the real ssh and a real sshd** (`internal/sshtunnel/real_test.go`).

- `TestRealSSH` — one tunnel, one login: ping, list 40 containers, a mutation and its event,
  **40 concurrent statistics streams**, an interactive shell; the local socket is `0600` in a
  `0700` directory; ssh was started once. The 40 is the point: it is four times what `sshd`
  allows per connection as *sessions*, and the reason the desk does not use
  `docker system dial-stdio` (DECISIONS R2).
- `TestRealSSHFailures` — a key the server does not accept; an unknown host key; nothing
  listening on the port. Each fails with ssh's sentence.
- `TestRealSSHExplain` — no socket at the path; a file that is not a socket; a socket in a
  directory the user may not enter; a socket the user may not open; a server that forbids
  forwarding; a socket nobody listens on; a server that runs no commands. Each gets the
  message DECISIONS R8 promises — one cause where one can be known, both where two cannot be
  told apart, all where nothing can be asked.
- `TestRealSSHReconnect` — the server and every connection it holds are killed; the tunnel
  reports why it is down; the server returns; the tunnel is back without being touched.

**The launch sequence, in process** (`cmd/docker-desk`). The program's own `run` with `--check`
— flags, resolution, transport, first requests, messages — for a local socket, TCP, TLS (two
ways in, six ways to fail), SSH (works; no socket; cannot log in; no ssh program; a daemon
that accepts and never answers; a socket path ssh would expand), and ten refusals.
`TestDefaultContextFollowsTheCLI`. `TestLaunchLeavesNothingBehind`.
`TestContextWrittenByDockerItself` is the cross-check on the re-implemented context store: the
real CLI writes the context and uses it against the fake, and the desk must then agree with
it. (The CLI's other rules the desk follows — the default port, the default context, the
default TLS host — were each checked by running `docker` against a closed loopback port and
reading where it said it could not connect.)

**The desk** (`internal/ui`). `TestRemoteDaemonIsAnnouncedEverywhere` — for five kinds of
endpoint: the header at three sizes, a confirmation, a form, the picker, the help, the
too-small screen. `TestLocalSocketLooksAsItAlwaysDid`. `TestRemoteTagIsNeverDropped` — six
endpoints at 80 columns, read-only, connected and disconnected: the tag, the flag, the state,
and both ends of the endpoint. `TestLongEndpointNeverHidesTheTag` — a 90-character endpoint.
`TestLongContextNeverHidesTheTag`. `TestFallenTunnelExplainsItself`.
`TestConfirmationsFitTheSmallestTerminal` — nine confirmations at 80×24, on a local socket and
on a long remote endpoint with a long context name: the box fits, the daemon is shown in full,
the prompt and keys are on screen, what is typed is visible, and every listed object can be
scrolled to.

**The real binary on a pseudo-terminal** (`internal/e2e/remote_test.go`).

- `TestRemoteOverTCP`, `TestRemoteOverTLS` — the tag; live statistics; stop a container through
  its confirmation and see the engine's events; a shell; the daemon stopping and returning;
  and, for TLS, the same daemon refused without certificates before anything is drawn.
- `TestRemoteOverSSH` — the launch says it is opening a tunnel, on the ordinary screen; the
  desk shows `[ssh]`; exactly one ssh process; logs follow; a stop and its events; a shell;
  then the `sshd` is killed: `DISCONNECTED`, "The ssh link to … is down — retrying", the desk
  still navigable; the `sshd` returns and the desk reconnects by itself; after quitting, no ssh
  process, no socket, no directory.
- `TestSSHAsksOnTheTerminalBeforeTheDesk` — the client key has a passphrase. ssh asks for it
  on the ordinary screen; the test types it; the desk opens. Then the link is dropped: the
  desk says `Permission denied`, and the raw output contains the prompt exactly once.
- `TestJumpHostNeverAsksUnderTheDesk` — the review's scenario: the target is behind a jump
  host and both logins need the passphrase. Two prompts before the desk opens; the link is
  dropped and restored; four seconds of reconnection attempts later there has been no third
  prompt, the desk still navigates, and `q` quits it.
- `TestSSHFailuresDoNotOpenTheWorkspace`; `TestCtrlCWhileSSHConnects` (exit status 130, and the
  ssh stand-in — which ignores the terminal's own interrupt — ended by the desk);
  `TestHangUpCleansUp` (`SIGHUP`: exit status 0, no ssh, an empty temporary directory);
  `TestKilledDeskTakesItsSSHAlong`.

Screens from these runs are in [`docs/screens/`](screens/): `19` to `23`.

### Themes

- `TestAThemeChangesColourAndNothingElse` — one unmoving desk is drawn in each of the ten
  themes: all five kinds at 140×40 and 80×24, and a refused deletion. With colour stripped,
  every frame is identical to the `dark` one, cell for cell. With colour kept, each theme uses
  at least six distinct colours and no two themes the same set; `mono` uses none.
- `TestDefaultThemeIsTheOriginalPalette` — the seven original colour values.
- `TestThemesAreLegible` — contrast ratios computed as WCAG defines them, against the
  background each palette is meant for: text ≥ 3:1, unfocused borders ≥ 2:1, highlighted row's
  text ≥ 4.5:1 on the highlight, highlight ≥ 1.4:1 against the page. This test changed several
  palettes (DECISIONS T4).
- `TestThemesWithoutColour` — with no colour, all ten themes emit the same bytes, and the
  focus border, the `◆`, the `▸` and the spelled-out states are all there.
- `TestThemeKeyCycles` — the order, the wrap-around, the status line; a letter in the filter;
  nothing under an overlay; bound to no action; no request sent.
- `TestThemes` (the real binary) — with no setting, the original 256-colour accent and a
  reverse-video highlight and not one 24-bit sequence; with `--theme dracula` on a 24-bit
  terminal, Dracula's accent and selection colours; after `T`, Nord's; `DOCKER_DESK_THEME`;
  an unknown name refused before the alternate screen.

### Do these tests have teeth?

Checked by taking things out, as in round one:

- Without the parent-death signal, `TestKilledDeskTakesItsSSHAlong` fails with "ssh (pid …)
  outlived the desk that was killed". (Its first version *passed* without it — the harness was
  hanging up the terminal. See the journal, §16.)
- With a reconnecting ssh left attached to the terminal, `TestJumpHostNeverAsksUnderTheDesk`
  fails with "ssh asked 1 more time(s) after the desk had opened".
- With only `:` and `\` refused in a socket path — the first version's rule —
  `TestTarget` accepts `ssh://evil.example/${AWS_SECRET_ACCESS_KEY}`, its percent-encoded
  form, `%i`, a space and an escape sequence, and fails on each; `TestCheckDestination`
  likewise.
- With the confirmation laid out by the old rule, `TestConfirmationsFitTheSmallestTerminal`
  fails for five dialogs — two of them on a *local* socket, which is the overflow that predates
  round two.
- With the attach left to the library's own dial, `TestAttachIsBoundedOverTLS` does not
  return; the run is ended by the test timeout.
- The remote tag being truncated in a confirmation is how `TestRemoteOverSSH` failed the first
  time it was run; `TestLongEndpointNeverHidesTheTag` was written from that failure.
- The data race between ssh's messages and the program's own was found by the detector in
  `TestCheckOverSSH`, not by reading.

All of the above were restored afterwards; the suite was then run in full, with and without
the race detector.

### The independent review

The new code was given to a separate reviewer with ten rules and no account of why it was
believed to obey them. Unlike round one's, this review *ran* things — scratch tests kept
outside the repository, against the fake daemon, a throwaway sshd, and the `docker` CLI
pointed at listeners of its own. It came back with sixteen findings. Two were serious: the
ssh started for a jump host asking for a passphrase underneath the running desk, and ssh
expanding environment variables into a socket path that it then sends to the server. Both
rest on behaviour of OpenSSH that I did not know, which is the point of asking someone else.

Everything it found was fixed, each fix with a test that replays the reviewer's input —
except two points that were kept, with the reason written down. The table is in
[DECISIONS.md, R13](DECISIONS.md#r13--an-independent-review-of-the-new-ways-in), with what
the review examined and could not break, and what it did not examine.

The fixes themselves have not been reviewed by anyone but their author.

## Round four: image findings

The analyser is pure code over a byte stream, so most of it is proven without a daemon:
`internal/forensics/fixture` writes archives in both layouts a daemon produces, from a short
description, and the tests feed them to `forensics.Analyse`. The fixture's demo image is also
what the fake daemon serves for `shop/api:dev`, so the UI and end-to-end tests see the same
bytes.

| Property | Test |
| --- | --- |
| A credential added in layer 2 and deleted in layer 3 is reported as still in the image bytes, with layer, instruction and masked evidence — in the classic and the OCI layout | `TestDeletedSecretIsStillInTheImageBytes` |
| Both layouts, and manifest-first versus manifest-last order, give the same findings; every layer hashes to its diff ID | `TestBothLayoutsSayTheSame` |
| A present SBOM is one line; a nested index that references absent manifests and attestation blobs is skipped, never fatal | `TestAttestationsAndAbsentBlobs` |
| Without `manifest.json`, `index.json` leads to the config and the layers | `TestIndexOnlyArchive` |
| When the history does not map onto the layers, no instruction is shown | `TestInstructionsAreOmittedWhenTheHistoryDoesNotMap` |
| Config, filesystem, binary and layer rules, each on bytes built to trigger it (ELF headers, Go build info, capability xattrs, whiteouts) | `TestConfigRules`, `TestFilesystemAndBinaryRules`, `TestLayerRules`, `TestPackagedExamplesAreHints` |
| A secret's value prints as `[masked]` in every fmt verb and in JSON; only `Reveal` returns it | `TestSecretsNeverPrint` |
| Broken, truncated and empty input fails in the desk's own words, quoting nothing | `TestBrokenInputFailsInItsOwnWords` |
| Cancellation stops the read; progress counts every byte; bounds are noted, and rules that need complete data decline | `TestCancellation`, `TestProgressAndBounds`, `TestPerRuleCap` |
| The live join: privileged (as root, from the desk), host network, published ports, unpinned and moved tags (both directions), no digest | `TestLiveFindings` |
| Exporting through a read-only engine sends no mutating request and carries the platform | `TestReadOnlyExportsAnImage` (engine) |
| The tab renders and scrolls at 80×24, 120×30 and 120×40 with every frame exactly the terminal's size | `TestFindingsTabRendersAndScrollsAtBothLayouts`, and the existing `TestFrameGeometryAtEverySize`, which now opens the tab on every kind and size |
| A second open is drawn from the cache in the same frame, with no request and no job | `TestSecondOpenIsInstant` |
| One secret is revealed at a time and hides when the cursor moves, the order changes or the pane loses focus | `TestRevealIsOneAtATimeAndHidesWhenTheCursorMoves` |
| No secret in the job's title, status, log or error, in the status line, or in the job output dialog | `TestASecretNeverLeavesTheScreen` |
| Read-only mode completes the analysis and the live join with only GET requests | `TestFindingsInReadOnlyMode`; end to end, `TestImageFindings/25-image-findings-80x24` runs the binary with `--read-only` |
| The live half follows the daemon without re-analysing | `TestLiveJoinFollowsTheDaemon` |
| A remote image over the threshold waits for Enter; the analysis is a cancellable job; moving away cancels it | `TestRemoteLargeImageAsksFirst`, `TestAnalysisIsACancellableJob`, `TestMovingAwayCancelsTheRunningAnalysis` |

**Speed.** `BenchmarkAnalyse` (not part of `make test`) reads an archive of 2,000 plain-text
files: about 125 MB/s on this machine (4 cores, under load).

**Against the real daemon**, `make real-readonly-check` now also exports and analyses the
smallest image under 300 MB through a read-only engine (`TestRealDaemonExportReadOnly`). It
logs rule counts only. Run once during the build: Alpine 3, OCI layout, one layer, verified
against its diff ID — and it is how the `openssl.cnf` example password was found (DECISIONS
F13).

Screens: `docs/screens/24-image-findings.txt` (150×42) and
`docs/screens/25-image-findings-80x24.txt` (80×24, `--read-only`), captured by `TestImageFindings`
with nothing revealed.

## Round five: Space

The accounting is pure code (`internal/space`) over figures the daemon states, so most of it
is proven on small worlds built by hand; the fake daemon now models layers by chain ID, and
the real docker CLI was used to check that it reports them as Docker does.

| Property | Test |
| --- | --- |
| Chain IDs follow the OCI rule; the same layer on another base is another chain | `TestChainIDs` |
| History maps onto layers only by exact count (empty-layer entries skipped; a zero-sized layer that cannot be placed means no mapping) | `TestHistoryMapsOnlyByExactCount` |
| Shared bytes are grouped by the exact set of holders, each byte once | `TestSharedBytesAreListedOnce` |
| What a set frees: own layers, layers only the set holds, never a layer an outsider holds | `TestWhatASetFrees` |
| Layer sizes that do not add up to the daemon's are not used; the set figure becomes a floor from the daemon's unique sizes; an unread image makes every set a floor | `TestAnIndexThatDisagreesWithTheDaemonIsNotUsed`, `TestAnUnreadImageMakesSetFiguresAFloor` |
| `DiskUsage` decodes the account (all GETs, verbose); below API 1.52 it is marked derived and sums to the same totals | `TestDiskUsageAsTheDaemonStatesIt`, `TestLegacyDiskUsageIsMarkedDerived` |
| An ordinary container list never asks for sizes; `ContainerSizes` does, for all or for given IDs | `TestSizesAreOnlyMeasuredWhenAsked` |
| `ImageSizes` and `ImageLayers` (inspect and history, oldest first) | `TestImageLayersAndSharedSizes` |
| The four new methods are read methods; the wrapper still overrides all 18 mutations | `TestReadOnlyCoversEveryMutation` |
| The size format is go-units' `HumanSize`, the reclaimable column the CLI's | `TestCLISize` |
| `S` opens from every pane, keys never reach the panes behind, `S`/Esc restore focus, kind and highlight; S typed into a filter is a letter | `TestSpaceOpensAndClosesWhereItWas` |
| The view fits and scrolls at 80×24, 120×30, 120×40 and 200×50, every frame exactly the terminal's size, with a dialog over it too | `TestSpaceFitsAndScrolls` |
| The totals on screen are the daemon's, from one df request, with no writable-layer walk and no mutation; unique sizes plus the shared groups equal the daemon's image total | `TestSpaceTotalsAreTheDaemons` |
| An image whose layers are shared frees less alone than its size; "together" is exact and larger | `TestSharedImageFreesLessAloneThanItsSize` |
| Writable layers only after `m` — not on opening, not on `r`, not on the periodic refresh — as a job; both measurements cancel | `TestWritableLayersOnlyWhenAsked`, `TestSpaceMeasurementsAreCancellable` |
| Archive attribution from the cached report, no second export, no analysis started for this view | `TestArchiveAttributionReadsNoArchive` |
| A refresh that finds nothing changed ages nothing; a change makes everything older STALE; `r` makes the account current and leaves the old measurement stale | `TestFiguresTurnStaleAfterAChange` |
| Enter goes to an image, a container, a volume; a filter that hid it is cleared | `TestEnterGoesToTheObject` |
| `d` and `X` open the desk's own dialogs, which state what they free (unique size; nothing for an untag; the set; the measured writable layer; the volume's size); the confirmed request is the only mutation and is not forced; no prune endpoint | `TestSpaceReclaimUsesTheExistingDialogs`, `TestRemovalConfirmationStatesWhatItFrees`, `TestRemovalConfirmationWithoutAnAccountSaysSo` |
| Read-only: every figure and both measurements, no `d`/`X` in hints or help, the keys open nothing, only GET/HEAD | `TestSpaceInReadOnlyMode` |
| An older daemon's totals are labelled as the client library's sums | `TestSpaceOnAnOlderDaemon` |
| The containerd case — no layer sizes add up — is said, not guessed: `?` for group sizes, floors for sets | `TestSpaceWhenLayerSizesDoNotAddUp` |

**End to end**, `TestSpace` runs the binary at 120×40 and at 80×24 with `--read-only`, and
runs the real `docker system df` against the same fake: every size and reclaimable figure the
CLI prints is on the screen, character for character. It also opens a removal from the view
and checks the confirmation frees the unique size. Screens: `docs/screens/26-space.txt`,
`27-space-remove-frees.txt` and `28-space-80x24-read-only.txt`. Every other screen was
regenerated too, because the demo's images now have layers and new sizes.

**The CLI against the fake**, `make crosscheck`: `system df` (all four lines), `system df -v`
(shared and unique per image), `ps -s` (writable layer and virtual size) and `history` (an
empty-layer entry, and layer sizes). 70 checks pass.

**Against the real daemon**, `make real-readonly-check` now also runs
`TestRealDaemonDiskUsageReadOnly`: the account and every image's inspect and history, through
a read-only engine. Run once during the build, against Docker 29.9.2: df answered in under a
second; 18 images, the history mapped for all of them and their layer sizes added up to the
daemon's for none — the containerd image store (DECISIONS U3). It logs counts and sizes only.


## Round seven: image packages

The riskiest part — rpm's database, read by the desk's own SQLite reader — was built and proven
first, against a database taken from a real image and checked against rpm itself.

| Property | Test |
| --- | --- |
| A real `rpmdb.sqlite` (ubi9-micro, committed) yields exactly the 22 entries rpm 4.16.1.3 reports for it: name, epoch, version, release, arch, licence | `TestRealRPMDBYieldsWhatRPMReports` |
| A database SQLite itself wrote with 512-byte pages — multi-level table and schema b-trees, 40 kB headers through overflow chains, freed pages, epochs — reads as its 1,001 rows; the bound keeps 10 and counts 1,001 | `TestSyntheticRPMDB` |
| Cut short, bytes changed at random, a cycle in the b-tree, UTF-16, a bad header: the read fails whole or reads whole, never part, never a panic | `TestDamagedRPMDBFailsWhole`, `TestSQLiteRefusesWhatItDoesNotRead`, `TestRPMHeaderBounds` |
| 20,000 mutations of a small database through the reader *without* its recover: no panic | `TestMutatedRPMDBNeverPanics`, and `FuzzReadRPMDB` (run for minutes during the build) |
| The demo image: every state, layers 1, 2 and 4 attributed by the database's diff, epochs that one side omits, one undeclared finding naming three packages with layer 4's instruction, one drift hint | `TestDemoPackages` |
| An SBOM above the 4 MiB JSON bound (a 3 MiB string and 20,000 file entries before its packages) is read as it streams and joined | `TestSBOMAboveTheJSONBoundIsStillBrowsable` |
| Capped tables keep their true total, and nothing the cap hides is called undeclared or declared only; a text database above its bound is unknown, with the bound | `TestBoundsAreSaid` |
| zstd layers: installed unknown for all three managers, the SBOM's rows listed as not checkable, no package findings | `TestZstdImage` |
| A deleted database is unknown, not empty | `TestDeletedDatabaseIsUnknownNotEmpty` |
| distroless `status.d`: files add up across layers, whiteouts remove one, `.md5sums` ignored | `TestDistrolessStatusD` |
| apk and CycloneDX (nested component, licence ids), and an npm package that is not checkable | `TestAPKAndCycloneDX` |
| The real database inside an image, streamed: 19 both, `tzdata` undeclared, a declared-only package, `gpg-pubkey` not checkable | `TestRealRPMDBInAnImage` |
| A non-empty `rpmdb.sqlite-wal` makes rpm unknown; Fedora's `/var/lib/rpm` link hides the old copy, with or without a whiteout | `TestRPMWriteAheadLogMakesItUnknown`, `TestFedoraLayoutFollowsTheFinalFilesystem` |
| An unreadable copy between two layers makes the attribution unsure, and says why | `TestAttributionAcrossAnUnreadableCopy` |
| Control characters and bidirectional overrides from a database or an SBOM reach no string | `TestStringsFromTheImageAreSanitised` |
| The JSON scanner: escapes, surrogate pairs, cut-off and broken documents (no partial list), nesting, strings longer than its bound | `TestJSONScanner` |
| purl parsing and version comparison with epochs | `TestPURLAndVersions` |
| The view renders and scrolls at 80×24, 120×30 and 120×40, every frame exactly the terminal's size | `TestPackagesViewRendersAndScrollsAtBothLayouts` |
| Esc closes onto the desk exactly as it was — the screen compared line for line | `TestPackagesViewClosesOntoTheDeskAsItWas` |
| `/` by name, version, licence and purl; Esc while typing undoes; `s` through every state with the right counts; `o` through every order; the cursor stays on its package through all of them, and through a filter that hid it | `TestPackagesSearchOrderAndStateFilter` |
| Undeclared packages are a finding on the Findings tab | `TestUndeclaredIsAFinding` |
| Read-only: only GET/HEAD, no second export | `TestPackagesViewInReadOnlyMode` |
| zstd and a large SBOM, through the fake daemon into the view | `TestZstdImageBrowsesItsSBOM`, `TestLargeSBOMBrowsesInTheView` |

**End to end**, `TestPackagesView` runs the binary at 120×40 and at 80×24 with `--read-only`:
it opens the view from Findings, cycles the state filter, searches, scrolls to the last row and
closes onto the Findings tab, with one export and no mutating request. Screens:
`docs/screens/31-packages-disagreements.txt` and `32-packages-80x24-read-only.txt`.

**Against the real daemon**, `make real-readonly-check` now also runs
`TestRealDaemonPackagesReadOnly`. Run once, against Docker 29.9.2: ubi9-micro's archive,
streamed through the read-only engine, yields `/var/lib/rpm/rpmdb.sqlite` in layer 1 with the 22
entries rpm reported for it; the image carries no SBOM, so every row is not checkable.


Stated plainly, because a control plane deserves it:

- **No mutating request from the desk has ever been sent to a real Docker daemon.** That is a
  direct consequence of the constraint. What stands in its place: the requests are encoded by
  Docker's own client library; they were tested against a fake; and the fake was validated by
  Docker's own CLI. The first real `stop` or `remove` you confirm will be the first one ever.
  If you want to watch the tool act before trusting it with anything, `make sandbox` is for
  exactly that.
- **The interactive shell has not been opened in a real container.** Exec is a `POST`, so it
  is blocked in read-only mode by design. Its protocol handling was tested against the fake,
  whose exec behaviour was checked with the real `docker exec` and `docker exec -it`.
- **The desk has never been connected to a Docker daemon on another computer.** Every remote
  test ends at the fake daemon on this machine, over loopback. What that leaves untested is
  the network itself: latency, packet loss, a link that stalls without closing (the 30-second
  keep-alive is there for it, and was exercised only by killing the server), NAT, proxies.
- **No real daemon has been reached over TLS or SSH at all** — not even read-only. A real
  `dockerd` with `--tlsverify`, and a real remote `sshd` in front of a real socket, are first
  contacts still to be made; `--check`, then `--read-only`, is the order to make them in.
- **Password logins over SSH were not exercised.** An unprivileged `sshd` cannot check
  passwords. A key with a passphrase stands in for "ssh has a question to ask"; ssh asks both
  the same way, but only one was seen.
- **Jump hosts, agents, certificates and hardware keys were not exercised.** They are ssh's
  business, which is why the desk uses ssh (DECISIONS R3) — but "it is your ssh" was tested
  with one plain key.
- **OpenSSH 9.6 only**, client and server. Forwarding to unix sockets exists since 6.7; the
  messages the failure hints match on ("Permission denied", "Host key verification failed")
  are old and stable, but other versions were not run.
- **The remote probe assumes a POSIX `sh` on the remote machine.** Anything else gets the
  message that lists every possibility.
- **The fixes made after the review were not reviewed again.** They are tested, and the tests
  were seen to fail without them, but the second pair of eyes saw the code before them.
- **What may be in an SSH endpoint is a guess at "safe"**, erring narrow: letters, digits and a
  few marks. It refuses things ssh would accept (a space in a path, a `~`), and it rests on the
  characters it permits meaning nothing to ssh — true of OpenSSH 9.6, and of what its manual
  says about tokens and variables.
- **A `ProxyCommand` that finds another way to ask** — a graphical prompt of its own, say — is
  outside what "no controlling terminal" prevents.
- **Theme colours were computed, not looked at.** Contrast ratios and escape sequences were
  checked; no palette was judged by eye in a terminal, and the backgrounds they were measured
  against are the schemes' published ones, not yours.
- **The light-background detection** relies on the terminal answering a query; it was tested
  only for "no answer means dark".
- **Only Linux was tested**, with Docker Engine 29.8 (API 1.56). macOS exposes the same unix
  socket and should work; it was not run there. Older engines are reached through API
  negotiation, also untested.
- **Screens were checked on a terminal emulator library**, not by eye in iTerm, Kitty,
  Alacritty, tmux, and so on. The frame-geometry and glyph-width tests exist to make that as
  safe as it can be without the eye.
- **Scale beyond the test sizes** (hundreds of containers, tens of thousands of images) was
  not measured. The statistics-stream cap was tested at 40 containers.
- **Image findings were checked against one real archive** (Alpine, OCI layout), read-only.
  The classic layout, gzip layers and attestations were proven on archives built from the
  specifications; images with thousands of layers' worth of files, zstd layers (not walked),
  and Windows images were not tried. The secret patterns are examples of well-known formats,
  not a complete catalogue, and say nothing about whether a value is live.
- **Space's shared-byte attribution has been seen to work only on the fake.** On the one
  real daemon it met (containerd image store) it correctly declined for every image; a real
  classic-store (overlay2) daemon, where the history's sizes should add up, was not available.
  Multi-platform images, whose `Size` in the containerd store spans the platforms present,
  were not examined, nor were volumes on non-local drivers (reported as not measured).
- **How slow `/system/df` is on a busy engine** was not measured beyond the one real daemon
  (under a second, four containers). The delay was simulated in the fake to prove the
  cancellation, not its timing.
- **Event replay after a reconnect** was tested against the fake's event history; the real
  daemon keeps a similar bounded history, but its behaviour across a daemon *restart* (history
  lost) relies on the full refresh that follows every reconnect.

## Round six: container processes

- `fakeengine.TestTopHTTP` — the fake's `/containers/{id}/top` over raw HTTP: default `ps -ef`
  listing (deterministic), empty, ragged columns, 409 stopped, 404 missing, injected 500, and
  the request log.
- `engine.TestProcesses` — the Moby adapter: mapping, empty rows, variable columns,
  `IsConflict` for a stopped container, `IsNotFound`, the daemon's message preserved, no
  `ps_args` ever sent, pass-through in read-only mode, `IsUnavailable` with the daemon down.
  `readMethods` lists `Processes`, so `TestReadOnlyCoversEveryMutation` still counts 18
  mutations.
- `ui.TestProcessesLifecycle` — the witness is the fake's count of `/top` requests: none
  before the tab opens, one on entry, none while it sits visible across list refreshes, one
  on `r`, one per highlight change (old rows gone first), none on `r` with another tab
  shown and one on the next visit; nothing reaches jobs or the status line; no mutation.
- `ui.TestProcessesStates` — stopped (no request, no dialog), empty, the 409 race, a general
  error replaced by a later `r`, and a crash after a snapshot hiding the rows.
- `ui.TestProcessesStaleOnDisconnect` — rows kept and marked stale with the daemon down,
  taken again after it returns.
- `ui.TestProcessesRejectsLateAnswers` — an answer for the previous container, or an
  overtaken one for the current, is never drawn.
- `ui.TestProcessesTableAndScrolling`, `ui.TestProcessTableCells` — custom titles, a
  300-character command, extra and missing cells, escape and control characters, frame
  geometry at four sizes, and line/page/top/bottom scrolling.
- `TestFrameGeometryAtEverySize` walks the new tab for every kind at all nine sizes.
- `e2e.TestProcessesView` — the real binary at 140×40 and 80×24: open, refresh, another
  container, a stopped one; screens 29 and 30.

## Round eight: image changes

- `changes.TestFindPairRefusals` — the pair from a reference: a moved tag, the default
  registry and `:latest` normalisation, a digest, an image ID (full and short), a tag that no
  longer resolves, the same image; and the platform refusal.
- `changes.TestLineage` — base intact with layers added, divergence, no layer in common, a
  history that does not map (no instruction, the reason said), and a build argument's value
  kept out of the instruction.
- `changes.TestConfigNeverCarriesEnvValues` — every config field, labels added, removed and
  changed, and environment names only: no value in any change.
- `changes.TestContent` — packages added, removed and version changed; a pseudo-package
  skipped; findings that appeared and disappeared, a layer finding whose share changed not
  counted as either; an unknown database said, not compared; an unreadable layer refuses.
- `engine.TestImageFactsKeepsNoEnvValue` — two GETs per image, the decoded fields, and no
  environment value anywhere in the struct, printed every way. `readMethods` lists
  `ImageFacts`, so `TestReadOnlyCoversEveryMutation` still counts 18 mutations.
- `ui.TestChangesMovedTag` (120×40 and 80×24) — the witness is the fake's request log: at most
  four requests on opening, exactly four image reads, no export before Enter; the shared
  prefix, the added layers with their instructions, the config difference; after Enter two
  exports, the package and finding differences, the token masked, the job succeeded; leaving
  and returning draws in the same frame with no request; both reports in Findings' cache.
- `ui.TestChangesEveryTheme` — every theme at 80×24 and 120 columns, frame geometry intact.
- `ui.TestChangesCancelAndRetry` — a slowed export cancelled with `c`, said, and retried.
- `ui.TestChangesRefusals` — on `SeedChanges`: pinned, created from an ID, unresolved, the
  same image, another platform (Enter reads nothing for any of them), an unreadable zstd
  layer after Enter, and a rebuild on another base.
- `ui.TestChangesReadOnly` — the whole flow with `--read-only`: no mutation, only GET/HEAD.
- Every UI test above checks, through `noEnvValue`, that no environment value of either build
  is on screen, in the status line, or in any job's title, status, error or log.
- `e2e.TestChangesView` — the real binary at 120×40 and at 80×24 read-only: no export before
  Enter, the comparison, no environment value on screen, only GET/HEAD; screens 33 and 34.

What these do not show: a real daemon's moved tag. The fake serves the two builds' real
archives, inspect and history consistently, but the comparison has not been run against
images pulled from a registry.

## Round ten: search in the Details pane

- `search.TestFindFoldsCaseAndDoesNotOverlap`, `TestNoRegularExpressions` — case folded rune by
  rune with byte offsets of the text itself (`É`, `ß`, the Kelvin sign); a query is never a
  pattern.
- `search.TestMatchingStripsEscapes` — a word split by a colour change matches; the bytes of a
  sequence (`31m`, `[0m`, ESC) never do.
- `search.TestMarkHighlightOffsetsInStyledText`, `TestMarkWideAndMultibyte`, `TestClip` — marks
  land at stripped offsets inside styled text, a colour inside a mark re-asserts it, the line's
  colour is restored after it; wide runes; the part of a match on one wrapped row.
- `keypath.TestPaths`, `TestClosingLinesAndRoot`, `TestTolerant` — nested objects, arrays and
  arrays of arrays as `[n]`, keys that need quoting, closing lines, fragments and malformed input.
- `ui.TestFindHighlightOffsets` — matches marked in a styled Info-like line, current one apart.
- `ui.TestFindSeeksTheWrappedRow` — a match in the third wrapped row of a line: the handler's
  request leaves the offset alone, `render` brings that row (not the line's first) into view, at
  two widths; a match broken by the wrap is marked on both rows.
- `ui.TestFindNextPrevWrapAround` — `n` past the last goes to the first, `N` before the first to
  the last; presses between frames add up; while typing, the current match is the first below
  the top row.
- `ui.TestFindIsMemoised` — the same lines set again keep the version and the very same matches.
- `ui.TestInspectFind` — the desk: live count, `at State.Status`, Enter, `n` to
  `at State.Health.Status` with its row on screen, wrap-around both ways, a quoted label key,
  `no match in the N lines`, and the bar gone after Esc.
- `ui.TestSearchBarCostsNoRowWhenClosed` — at 80×24, opening and closing the bar leaves the
  frame identical; open, the frame keeps its geometry.
- `ui.TestSearchEscLadder` — Esc while typing clears and stays; Esc again goes to the List; the
  same after Enter.
- `ui.TestSearchTypingNeverActs` — on Containers, Images, Volumes and Networks: every action key
  of the kind, `q ? S T r 1–5 [ ] Tab` and the arrows typed into the bar: no dialog, no plan, no
  quit, no view, no change of pane, tab or theme, no mutation; the keys are the query. With a
  query kept, `n` does not open "new"; after Esc it does.
- `ui.TestSearchClearedWithTheObject` — per-tab queries; another object clears them while the
  bar stays open, and the next `t` typed stops nothing.
- `ui.TestLogsFilterAndFollow` — hidden as typed, `● following · 6 of 61 match`; a new match at
  the bottom, a non-match only counted; scroll-back by matches, holding while a match arrives;
  `g` to the oldest match, `↓` back to following; `no match in the 64 lines held`.
- `ui.TestMatchingStripsANSIWithColourOn` — colour drawn: `error in w` matches across colour
  changes in Logs; `31m` and `[2;` match nothing in Logs or Info; the mark is underlined.
- `ui.TestMatchStyleInEveryTheme`, `ui.TestThemesAreLegible` — bold and underlined in every
  theme, current reversed, accent colour at ≥ 3:1 on each theme's background; brackets with no
  colour.
- `ui.TestSearchSendsNothing` — on a volume's Inspect, normally and with `--read-only`: typing,
  Enter, `n`, `N`, Esc add nothing to the fake's request log, and the bar reads the same.
- `e2e.TestSearchInTheSandbox` — `make sandbox` itself on a pseudo-terminal: find in shop-db-1's
  Inspect (screen 35), filter `error` in noisy-logger's Logs under 20 000 lines a second
  (screen 36), `no match in the 5000 lines held`, and Esc back to every line.

What these do not show: how the marks look on a real terminal (the pseudo-terminal capture is
text only; the unit tests check the sequences written).

## Round eleven: image freshness

No test reaches a real registry. `testguard.CheckRegistry` refuses, loudly, every registry dial
that is not a loopback address once the guard is installed (`registry`, `freshness`, `ui` and
`e2e` install it); `registry.TestGuardRefusesRealRegistry` proves a reach for Docker Hub fails
with *test guard*. Every registry in the suite is `internal/fakeregistry`: an in-process OCI
registry on 127.0.0.1 with indexes, manifests and configs, a Bearer realm on a second port (a
different host), Basic, a stale-token refusal, a quota with `Retry-After`, and a request log.

- `secret.TestSecretNeverFormats`, and the whole existing suite unchanged after `forensics.Secret`
  became an alias.
- `dockerctx.TestConfigDir` — `$DOCKER_CONFIG`, `~/.docker`, blank, neither.
- `registry.TestResolveCredentialKeys`, `TestResolveCredentialTokens` — the three-row key table,
  Docker Hub's legacy key, an empty user name, a password with colons, `identitytoken` and
  `registrytoken`.
- `registry.TestCredentialHelper`, `TestCredentialHelperFailures` — a fake
  `docker-credential-*` on `PATH` that records its standard input (the server key); non-zero
  exit, garbage, a helper that sleeps past its bound, a missing one, a name with `..`: the
  error says how it failed and never contains what it printed.
- `registry.TestParseRef` — bare names, `docker.io/…`, a host with a port, digest-only
  (*not applicable*), tag and digest.
- `registry.TestResolveRequestsAndMediaTypes` — all four media types sent; a current image costs
  one request, a moved one three; the config cached by digest; a platform missing from the index.
- `registry.TestBearerChallengeAndTokenCache`, `TestBasicChallenge` — anonymous, realm, Bearer;
  the token reused; a stale token dropped and fetched once; a wrong credential refused at the
  realm and absent from the error.
- `registry.TestTypedErrors` — not found, quota with its reset, 403, 500, name, TLS (an
  untrusted certificate), timeout, connection refused: asserted by kind, never by message.
- `registry.TestBoundsAndRedirects` — an oversized body cut off; a cross-host redirect that must
  not receive `Authorization`; no redirect to plain HTTP; a config whose bytes do not match its
  digest; `--no-registry` sends nothing.
- `freshness.TestVerdictStates` — current, the explored 13-layer pair (11 shared), earlier,
  identical, absent `created`, the epoch, gone, digest-only, built here; `newer` and `stale`
  appear nowhere.
- `freshness.TestRepoDigestsNotID` — containerd store (ID is the index digest) and classic store
  (ID is the config digest) reach the same verdict; with no `RepoDigests`, not applicable.
- `freshness.TestCaches` — a second open sends no request and keeps the age; `r` replaces it.
- `freshness.TestFallbackDecision` — transport and quota fall back; current, moved, gone, `401`
  and `403` do not; both failing gives one line with both reasons; *cannot ask* is not *gone*.
- `freshness.TestFallbackCredentialEncoding` — `X-Registry-Auth` equals `authconfig.Encode`'s
  output byte for byte.
- `freshness.TestBatch` — the third request refused for quota stops the batch, answers kept;
  cancelled after the first, no second request.
- `engine.TestDistributionInspect`, `TestDistributionInspectReadOnly`, and
  `DistributionInspect` in the read-only coverage list — at the fake's own API version and at
  1.40; gone, denied, and the endpoint not served.
- `ui.TestUpstreamSandboxFallback` — the sandbox's three tags through the daemon; reopening asks
  nothing; `r` asks again; the column's words; an unchecked image is blank.
- `ui.TestUpstreamLayouts` — 80×24, 120 and 200 columns, every theme, read-only and not; the
  column drops before any CLI column.
- `ui.TestUpstreamBatchQuotaAndCancel`, `TestNoRegistry`, `TestUpstreamReadOnlyCompletes`.
- `ui.TestNoCredentialAnywhere` — the tab, Inspect, the list, help, and every job's lines,
  status and error searched for the fixture's secret, its base64 and a rotated password.
- `e2e.TestCredentialsAgreeWithTheCLI` — the real `docker login` (sent to the fake daemon's
  `POST /auth`) into a temporary `DOCKER_CONFIG`: the server keys, the base64 shape and the
  helper path the desk reads are the CLI's. Skipped, saying so, where `docker` is not installed.
- `e2e.TestImageUpstream` — the real binary on the sandbox seed, wide and 80×24 read-only;
  screens `37-image-upstream`, `38-image-upstream-80x24`.

## Round twelve: the repository browser

Every registry in these tests is `fakeregistry` on 127.0.0.1 (the test guard refuses anything
else), counting its requests; the daemon is the fake engine.

- `registry.TestListTagsFollowsLink` — 250 tags in three pages, three requests, queries
  `n=100`, `n=100&last=t099`, `n=100&last=t199`; a cursor for another repository refused with no
  request. `TestListTagsRefusesAnOffHostLink` — a `Link` to another host is not followed.
  `TestListTagsDropsInvalidNames` — an escape sequence in a tag name is dropped and counted.
- `registry.TestRepository401Wording` — a repository answering 401 whatever the credential, on
  the tag list and a manifest, anonymous and credentialed: the sentence, and no claim of
  absence; a 404 says so with its code.
- `registry.TestDescribeIsOneRequest` — an index's platforms in one request; a single manifest.
- `repository.TestNaturalOrder` — a table, and `Less` checked as a total order on it.
- `repository.TestListingCap` — ten pages reach 1000 and stop; a 1500-tag page is cut at 1000;
  a short listing is not capped.
- `repository.TestJoinOutcomes` — here, the tag moved upstream, same build as (the first tag
  naturally), `-`; another repository's digest, an image built here, and an image whose ID equals
  the digest join nothing; a digest-pulled image is here on both stores.
- `repository.TestBuildTimesQuotaAndCancel` — the three-request bound, the epoch, a missing
  platform; a quota of four stops after one tag's three requests and the fourth's refusal, the
  first answer kept; cancelled after two.
- `plan.TestPullPinned` — the exact reference, `TierTyped`, the word is the tag, the effects,
  Docker Hub's familiar name, the platform blocker, and four malformed inputs.
- `ui.TestRepositoryNoRequestOnOpen` — no image: an empty name line, no request; `qdBe?/` typed
  as text; Esc steps out, then closes onto the same pane and kind; a typed name asks one tag list.
- `ui.TestRepositoryOnePerHighlightedRow` — one tag list and one manifest on open; one more per
  row rested on; none on returning; five rows passed quickly cost one; the desk restored on
  close; reopening asks no kept description again.
- `ui.TestRepositoryJoinAndPlatforms` — the sandbox seed's eight rows; the arm64-only tag refused
  before anything is sent; natural order; every theme.
- `ui.TestRepositoryPagingAndFilter`, `TestRepositoryPinnedPull` (a wrong word refused; the tag
  accepted; the fake daemon receives `tag=sha256:…`; the row then says `here`),
  `TestRepositoryReadOnly`, `TestRepositoryNoRegistry`, `TestRepository401`,
  `TestRepositoryBuildTimesQuota`, `TestUpstreamMovedPullsByDigest` (the same plan from `P`),
  `TestRepositoryLayouts` (80×24, 120×40, 200×50, read-only and not, every theme, the name line
  and the filter open).
- `e2e.TestRepositoryBrowser`, `TestRepositoryBrowserCompactReadOnly` — the real binary; screens
  `39-repository`, `40-repository-pinned-pull`, `41-repository-80x24-read-only`.

## Round thirteen: evidence export

The daemon is the fake engine with the sandbox's seed (`SeedDemo`, `SeedUpstream`), through a
read-only engine wherever the test makes its own; every file is written into the test's own
temporary folder (and, end to end, into a `HOME` of the test's own).

- **Golden files per format** — `collect.TestGoldenPerFormat` analyses `python:3.12-slim` (every
  package state) and `shop/api:dev` (every findings group) and takes the Space account, as
  `--report` does, and compares `json`, `spdx`, `cyclonedx` and `text` (Space: `json`, `text`)
  with `internal/report/collect/testdata/` byte for byte. `go test ./internal/report/collect -update`
  rewrites them; a diff in review is a change in what a reader of these files gets. No mutating
  request is sent.
- **Determinism** — `collect.TestTwoExportsAreIdentical`: two analyses of each image, two accounts,
  the same bytes in every format. `report.TestEveryFormatIgnoresInputOrder`: twenty shuffles of
  every list per format, the same bytes, and the caller's slices left in their order.
  `report.TestPackagesByNameThenVersion`, `TestTextIsOneLinePerFinding` (a value with a line break
  and a tab stays on its line; findings of a group by path; masked values said),
  `TestSpaceCarriesMeasures` (exact / at least / unknown and the reason on every figure, in JSON
  and text; SPDX and CycloneDX refused for Space).
- **The formats' shapes** — `report.TestSPDXShape`: every field SPDX 2.3 requires, SPDXIDs unique
  and of the allowed characters, the fixed creation time, a namespace without `#`; a package with
  no version gets no `versionInfo`; npm and no-purl packages stay *not checkable*; a *version
  differs* row carries no purl beside the installed version. `TestCycloneDXShape`: the 1.5 header,
  the schema's serial-number pattern, unique `bom-ref`s, a state on every component, a different
  serial for different content. `TestNoPackageSet`.
- **What a file must not hold** — `collect.TestNothingAFileMustNotHold`: every document of the
  sandbox is searched for the demo's secret values (revealed from the report, the only place they
  exist), environment values, every container ID, the daemon's socket, address and name, and
  today's date. None is there.
- **Refusals** — `report.TestWriteFileRefusals`: an existing file and a symbolic link (unchanged
  afterwards), a folder (with force too), a missing folder, a parent that is a file, no path — each
  a one-line `WriteError`, and nothing left in the folder. `TestWriteFileUnwritableParent`: a 0500
  folder, permission denied, nothing left (skipped as root). `TestWriteFileCreates0600` under a
  umask of 0. `TestWriteFileForceReplaces`. `TestExpandPath`, `TestDefaultPathAndExtensions`.
- **Headless** — `main.TestReportHeadlessWritesAndExits`: `--read-only --report` exits 0, says
  where, writes 0600 and a parseable document, sends no mutation; a second export is the same
  bytes; every format and `:space`. `TestReportHeadlessRefusals`: an existing file (left as it
  was), a folder, a missing folder, an unknown image → exit 1; flags that do not fit → exit 2;
  nothing on stdout, nothing left; `--force` replaces with 0600. `TestReportHeadlessLeavesNoPartialFile`:
  the fake fails `GET /images/get` part-way → non-zero, and the folder is empty; an unwritable
  folder → exit 1, empty.
- **The dialog** — `ui.TestExportFindingsIsTheCommandLinesDocument` (read-only): the prefilled
  name, the formats, the absolute path asked about before anything is written, `y`, mode 0600, and
  the file equal to `collect.FetchImage`'s for the same image; `Tab` to text swaps the extension.
  `TestExportRefusalsNeverBlockTheDesk`: an existing file and a folder refused in the dialog, `~bob`
  refused, the file unchanged; a 0500 folder fails after consent with one sentence on the status
  line, nothing left, and the tab still takes keys. `TestExportConsentIsNotTypedAhead`: with the
  arming delay on, a `y` typed with the question is not consent; `n` goes back.
  `TestExportPackagesAndSpace` (read-only, 80×24): SPDX prefilled in Packages; Space offers json
  and text only and writes `collect.FetchSpace`'s bytes; neither view closes.
- **End to end** — `e2e.TestExportFromTheSandbox` drives `make sandbox` itself with a `HOME` of its
  own: `w` on `python:3.12-slim`'s Findings tab, in its Packages view and in Space, each through the
  confirmation; then the binary make built runs `--report` against the same sandbox, while it runs,
  for the image (json, cyclonedx) and `:space` (text). Every file is parsed: the schema, the
  reference and platform, the three supply findings, `libssl3` *version differs*, `curl`
  *undeclared* in layer 4, `pip` and `requests` *not checkable* (pypi) with a reason, `python`
  *not checkable* for want of a purl; SPDX packages by name then version, as many as the rows;
  CycloneDX 1.5 with as many components; Space's four totals, its groups and figures each with a
  measure; mode 0600; no host path. The TUI's image document and `--report`'s are the same bytes.
  Screens `42-export-findings`, `43-export-confirm`.

## Round fifteen: the Files view

Everything below runs against the fake engine and in-memory archives built by
`internal/forensics/fixture`, except the two opt-in real-daemon checks at the end. Nothing in the
suite can mutate a daemon; the view sends only `GET`s, and the tests say so.

**What is proven**

- **The headers-only mode** — `forensics.TestFilesModeReadsHeadersOnly`: an archive whose files
  carry an AWS key, a setuid binary and a dpkg database yields, in `ModeFiles`, no finding and no
  package list (the same archive in the default mode yields all three, so the silence is the
  mode's); `ModeBoth` makes the same findings as the default and the listing too.
  `TestFilesModeAllocatesNoContentBuffer`: the buffers file bodies are read into do not exist in
  `ModeFiles`. Every analysis test that predates the round passes unchanged: `ModeFindings` is the
  zero value.
- **The listing** — `TestListingPerLayer`: three layers, both layouts, `manifest.json` first and
  last — a replaced file, a whiteout, an opaque directory, a symlink and a hard link give exactly
  the expected entries per layer, in the manifest's order. `TestListingBounds` sets each bound tiny
  and sees each dropped count; `TestListingPathAtLinuxsBound`: a 4,096-byte path is kept, 4,097 is
  dropped as the length bound's. `TestInstructionByLayer`: layer 7 of 12 says its `RUN`, an empty
  history entry and an unmapped history say `unknown`. `TestListingNamesAZstdLayer`: the zstd
  layer has no entry and is named; the gzip and plain layers beside it are complete.
- **The merge** — `files.TestMerge` (a table): replace (the later layer is the writer), remove
  (the count is the whiteouts that hit a held path, not those that hit nothing), a removed
  directory (what was beneath it goes, and is counted as hidden), opaque (only the marking layer's
  and later entries survive), a directory several layers wrote, a hard link (no bytes of its own),
  a file replaced by a directory and back. `TestMergeNamesAreKeptUnderTheRoot`: `../../etc/passwd`
  is `/etc/passwd`. `TestMergeSymlinkIsNotFollowed`: a symlink to a directory is a leaf even when
  asked to open; a device is typed. `TestDirectorySizes`: three files add up to the directory's
  computed size, and the counts match.
- **Rows, cursor, filter** — `TestFlattenShowsOnlyWhatIsExpanded` (by name, by size);
  `TestCursorFollowsItsPath` (across a re-order, a collapse of its parent and the re-open, and
  every movement); `TestFilterShowsAncestors` (a deep match with its directories, case ignored,
  the number searched, `MayMiss` when the index is cut).
- **The escaping** — `files.TestEscape`: names with ESC, CR, LF, `\xff\xfe`, tab, DEL, a C1 CSI and
  a backslash; no raw control byte is left; real non-ASCII names and a literal U+FFFD are kept.
  On screen: `ui.TestFilesHostileNameIsDrawnEscapedOnOneLine` (the sandbox's
  `\x1b[31mred\nforged-row\xff.html`: escaped, on one line, and the raw sequence nowhere in the
  frame); in the file: `ui.TestFilesExport` (text and JSON, neither holding the raw bytes, nor
  JSON's `\u001b` or U+FFFD in their place); end to end, `e2e.TestFilesInTheSandbox` searches the
  terminal's raw output for the name's raw bytes.
- **The overlay** — `files.TestApplyMarksEachKind` (changed, added, deleted, an added directory
  whose entry beneath is counted and not drawn, an implied directory; the image's own tree left
  exactly as it was), `TestApplyCleansPathsUnderTheRoot`, `TestApplyOverACutIndex` (a deleted path
  the index lacks is counted, not drawn), `TestApplyWithNoImage`. In the view:
  `ui.TestFilesContainerBeforeAndAfterTheImage` — the changes alone, without sizes, before the
  image is read; the tree with the changes over it after, without asking the daemon again; each
  kind's row and detail (the struck-through row checked with colour on); `r` sends one change
  request and no archive request. `TestFilesContainerWithNoChangesAndAFailedList`.
- **The request log** — `ui.TestFilesReadsNothingUntilEnter`: opening the view on an image sends
  **no request at all**; `Enter` sends one `GET /images/get`, and reopening sends none.
  `TestFilesReadIsCancellable`: `c`, and closing the view, cancel the job; no index is kept.
  `TestFilesReadOnlySendsOnlyGETs`: read-only, a container's view, the archive, `r` and `w` —
  every request a `GET`. `engine.TestContainerChanges`: one `GET /containers/{id}/changes`, kinds
  0, 1 and 2 decoded, read-only lets it through, no mutation. `make crosscheck` compares the real
  `docker diff` with the fake's list for `shop-web-1`, line for line.
- **The view** — `TestFilesOpensAndClosesWhereItWas` (both kinds, from every pane, `F` on a volume
  is one sentence), `TestFilesTreeAt80Columns` (expand, order by size, the columns, `←` to the
  parent and closed, a symlink that does not open, the frame's geometry), `TestLeftFitKeepsTheBasename`,
  `TestFilesFilterShowsAncestorsAndClearsOntoThePath`, `TestFilesFilterSaysItMayMiss` (a zstd
  layer), `TestFilesDetailNamesTheLayerAndItsInstruction` (`7 of 12` and its `RUN`; a changed
  file's size is the image's), `TestFilesHelpAndFooter`.
- **End to end** — `e2e.TestFilesView`, at 120×30 and at 80×24 read-only: `python:3.12-slim` ›
  Files reads nothing on opening; `Enter`, `c` (cancelled, nothing kept), `Enter` again; a filter
  to the deep `requests/__init__.py`; `w` to a text file in a `HOME` of the test's own, parsed;
  then `shop-web-1`'s change list alone, and over its image. Three archive requests in all (one
  cancelled), one change request, nothing but `GET` and `HEAD`. `e2e.TestFilesInTheSandbox` drives
  `make sandbox` itself: the nginx tree's whiteout count, the hostile name escaped, a symlink, a
  device and a FIFO, and every overlay state on `shop-web-1`. Screens `47-files-not-read`,
  `48-files-filter`, `49-files-container-changes-only`, `50-files-container`, and the 80×24 ones.

**Measurements**

- An index at the entry bound — 200,000 entries built from a listing in memory,
  `files.TestTotalsAtTheEntryBound` — is built in **0.4 to 1.2 s** on this machine (three runs:
  1.19, 0.98, 1.02 s; an earlier one 0.40 s) and holds **55 MB** of heap after a collection,
  every run. The design estimated ~25 MB; the node is larger than that estimate assumed (V11). The
  view keeps two, so its worst case is about 110 MB.
- A real image (below): 10,330 entries, 66.6 MB of archive read and indexed in 3.4 s.

**What is not proven**

- **A container's own archive is never read**: no test calls `GET /containers/{id}/archive`, and
  no code does — the engine has no method for it. A changed file's own size and an added
  directory's contents are therefore never shown; the view says so.
- The **suite** touches no real daemon. Two opt-in checks do, read-only:
  `engine.TestRealDaemonFilesReadOnly` (in `make real-readonly-check`) and
  `e2e.TestRealDaemonFilesTour`. They were run once against this machine's Docker 29.9.3 with
  a before/after snapshot of its objects, unchanged; the outcome is in
  `openspec/changes/files-explorer/verification.md`. That is one daemon, one storage driver and
  five images, not a survey.
- The 200,000-entry bound is measured on a synthetic listing, not on an image that large: no such
  image was read.
- Layouts other than the two the fixture writes (the classic archive and Docker's OCI layout) are
  not tested here, beyond what the Findings rounds already test of the same walk.

## Round seventeen: creating a container

The create form is a mutation that adds an object, in three dependent steps. Each layer is proven
where it can be, and the fake daemon's request log is the witness everywhere above the planner.

| What | Proven by | Command |
| --- | --- | --- |
| Parsing each entry: ports (tcp default, `udp`, bracketed IPv6, out of range, unbracketed IPv6), `NAME=value`, mounts (bind/volume, `ro`, relative destination), restart policies (`on-failure:3`, a retry on the wrong policy), the quote-aware command splitter | pure table tests | `go test ./internal/plan -run 'Parse\|Split'` |
| Which field and entry is wrong | `TestParseCreateNamesTheField`, `TestContainerCreateRefuses` | `go test ./internal/plan -run Create` |
| The plan: the pull present only when the image is missing, the dependency indices, `:latest` filled in, a name in use refused, a host port repeated on one address refused, no environment value in any description | `TestContainerCreate*`, `TestCreateNeverDescribesTheEnvironment` | `go test ./internal/plan -run Create` |
| Execution: a failed pull skips create and start; a failed create skips start; the start names the ID the create returned even after a rename and with an impostor under the old name; a start failure leaves a `created` container and says so; a cancel between create and start; a skip that cascades | `TestCreate*` and `TestDependencyOnASkippedStepIsASkip` in `execute_test.go`, against the fake daemon | `go test ./internal/plan -run 'Create\|Dependency'` |
| Existing plans unchanged: prunes, projects, selections, pins still process every object after a failure | the existing plan tests | `go test ./internal/plan` |
| The request body: image, name, ports (`ExposedPorts`, `PortBindings`, a daemon-chosen port), env, typed mounts, restart policy, command — and nothing else in `HostConfig` | `TestCreateContainerRequestBody`, `TestCreateContainerRefusals` | `go test ./internal/engine -run CreateContainer` |
| Read-only refuses it, in the wrapper and in the request hook; the tripwire counts 19 mutations | `TestReadOnlyCoversEveryMutation`, `TestReadOnlySendsNothing` | `go test ./internal/engine -run ReadOnly` |
| The fake's `POST /containers/create`: 404 absent image, 409 name in use, 400 invalid name or relative target (before any volume is made), 201 with the ID, a `created` container, the `container`/`create` event, then start | `TestContainerCreateHTTP` | `go test ./internal/fakeengine` |
| The fake against the real CLI: `docker create` with name, ports, env, mounts (`--mount` and `-v`), restart policy and command, `inspect` of each, a taken name, a relative target, `docker start`, a daemon-chosen name, the CLI's own pull after the 404 | 19 checks in `scripts/cli-crosscheck.sh` | `make crosscheck` |
| The form: opens sending nothing, with or without a selection, and from the menu; Esc records nothing; `Tab`/`Shift+Tab` wrap; `Ctrl+N`/`Ctrl+X` add and remove entries and never reach a text input; the four single-field forms keep their behaviour | `TestCreateFormOpensSendingNothing`, `TestFormTabWrapsAndShiftTabGoesBack`, `TestFormRepeatableRows` | `go test ./internal/ui -run 'Create\|Form'` |
| The summary restates every value before Enter; the job pulls, creates and starts; `TOKEN=s3cret` is in the request and the form, never in the job's title, target, status, error, output or the status line | `TestCreateSummaryThenSubmit`, `TestCreateWithPresentImageSaysSo` | `go test ./internal/ui -run Create` |
| A refused value keeps the form open, says why, and focuses the field and entry at fault: a bad port, a taken name (from the fresh snapshot), `=value`, a relative destination, an unparsable command; an Enter typed ahead of the form sends nothing | `TestCreateRefusedValuesKeepTheFormOpen`, `TestCreateEnterTypedAheadSendsNothing` | `go test ./internal/ui -run Create` |
| Read-only refuses `n` before a form opens | `TestCreateFormReadOnlyRefuses`, `TestReadOnlyDisablesEveryAction` | `go test ./internal/ui -run ReadOnly` |
| 80×24 with every field and three entries per list: the focused field, its hint, `field i of n` and every key stay on screen | `TestCreateFormFitsSmallTerminal` | `go test ./internal/ui -run CreateFormFits` |
| The real binary: a missing image is pulled, then created, then the returned ID started — exactly those three requests — and the row appears running; a present image is not pulled; a failing pull sends nothing else and the job names the skipped steps | `TestCreate`, `TestCreatePullOrNot` | `go test ./internal/e2e -run Create` |

Screens: `docs/screens/54-create-form.txt` (150×42) and `docs/screens/55-create-form-80x24.txt`
(80×24), captured by `TestCreate`.

What is not proven: the create was never sent to a real daemon — the suite cannot, by design, and
no opt-in check was added for a mutation. The request body is proven against the client library
and the fake, and the fake against the real CLI's `docker create`; that is the same chain of
evidence as every other mutation.

## Round eighteen: copying files between a container and this machine

A copy is the one place the desk reads or writes a file's contents, and copy in is a mutation.
The host-side rules live in one package and are proven there with hostile archives; above it, the
fake daemon's request log and its upload record are the witness, and the fake is held to the real
`docker cp`.

| What | Proven by | Command |
| --- | --- | --- |
| The walk: files, directories, links, bytes and skipped special files counted; a missing source, `/`, a FIFO root and an unreadable directory refused | `TestScanCountsTheTree`, `TestScanRefuses` | `go test ./internal/transfer -run Scan` |
| The tar: entries rooted at the name given, PAX long names, modes, no owners, a symlink as a link, a hard link as a file, a round trip to the same tree, a cancel mid-file | `TestTarRoundTrip`, `TestTarOneFileIsOneEntry`, `TestTarSendsAHardLinkAsAFile`, `TestTarStopsWhenCancelled` | `go test ./internal/transfer -run Tar` |
| The extraction: a file and a directory land at exactly the destination; an existing file, folder or link is refused and untouched; a missing parent is not created; links made last; an entry beneath the archive's own symlink refused whichever comes first | `TestExtract*` | `go test ./internal/transfer -run Extract` |
| Hostile archives: absolute names, `..` (also one that cleans inside), two top-level entries, a name twice, a file then a directory, hard links out of the destination, to another top, to a link; an empty archive; devices and FIFOs skipped and named, escaped — each asserting nothing changed in a temporary root | `TestExtractHostileNames`, `TestExtractSkipsSpecialFilesAndNamesThem`, `TestCheckName` | `go test ./internal/transfer` |
| Clean-up: a cancelled context and an injected `ENOSPC` remove everything written, a read-only directory included, walking the parent before and after | `TestExtractCleansUpWhenCancelled`, `TestExtractCleansUpAfterAWriteError` | `go test ./internal/transfer -run CleansUp` |
| The engine: the stream and the stat (name, size, mode, link target); a missing path is `KindNotFound`; the upload's query is `path=<parent>`, `noOverwriteDirNonDir=true`, no `copyUIDGID`; the tar the fake received; a missing parent is the daemon's 404; `/` and relative destinations refused before a request | `TestCopyFromContainer`, `TestCopyToContainer` | `go test ./internal/engine -run Copy` |
| Read-only: copy out streams; copy in is refused by the wrapper and by the request hook; the tripwire counts 20 mutations | `TestReadOnlyCopy`, `TestReadOnlyCoversEveryMutation` | `go test ./internal/engine -run ReadOnly` |
| The fake's `GET`/`HEAD`/`PUT /containers/{id}/archive`: a file, a directory, a symlink, a FIFO, a path through a symlinked directory, a missing path, an unknown container; uploads that replace, merge and record themselves and the change list; refusals (missing parent, not a directory, file over directory and the reverse, traversal, absolute) that change nothing | `TestArchiveGet`, `TestArchiveStatHEAD`, `TestArchivePut`, `TestArchivePutRefuses` | `go test ./internal/fakeengine -run Archive` |
| The fake against the real CLI: `docker cp` of a file, a directory (with a link) and a symlink out, modes kept, a missing path; a file and a directory (with a link) in, a missing parent, a file over a directory | 10 checks in `scripts/cli-crosscheck.sh` | `make crosscheck` |
| The plan: the walk's counts and words; `TierConfirm`; refusals with no steps built; the job copies a tree into the fake exactly; a file to another name is not nested; the daemon's refusals are the job's error; a source gone since the confirmation is the local error; no file's contents in the title, target, effects, description, status or output (a sentinel file) | `TestCopyIn*` | `go test ./internal/plan -run CopyIn` |
| The view: `o` and `i` on a container, the image sentence, a deleted path, read-only refusing `i` and offering `o`; the forms' prefilled destination, rule sentence, container and daemon; the local refusals (exists, missing parent, parent a file, `~user`) before any archive request; the absolute path shown for `~/x` and a relative path; copy out of a file, a directory with its link, a FIFO skipped and named, a daemon refusal leaving nothing; a cancel mid-stream leaving nothing; copy in confirmed only by `y`; the change list read once after the job, and not at all once the view is closed; the footer, the help and the README agreeing | `TestFilesCopy*`, `TestFilesHelpAndFooter`, `TestCopyKeysAreDocumented` | `go test ./internal/ui -run 'FilesCopy\|FilesHelp\|CopyKeys'` |
| The real binary: a file copied out of `shop-web-1` in read-only mode has the container's bytes and mode, with one archive `GET` and no mutation; a local directory copied in, captured form and confirmation, exactly one `PUT`, the fake's tree, the change list read again; an existing local destination refused before any archive request | `TestCopyOut`, `TestCopyIn` | `go test ./internal/e2e -run Copy` |
| A real daemon, read-only: one path of a named container copied out by the desk and by the real `docker cp`, the two trees identical (names, types, permission bits, link targets, bytes); copy in refused before the wire | `TestRealDaemonCopyReadOnly` (opt-in) | `DOCKER_DESK_REAL_HOST=… DOCKER_DESK_REAL_COPY_CONTAINER=<name> go test ./internal/engine -run TestRealDaemonCopyReadOnly` |

Screens: `docs/screens/56-copy-in-form.txt` and `docs/screens/57-copy-in-confirm.txt` (150×42),
captured by `TestCopyIn`.

What is not proven: a copy **in** was never sent to a real daemon. The test guard refuses, for
every test, to open a real daemon read-write (B3), and this round did not change that. The upload's
shape is proven against the client library and the fake, and the fake against the real CLI's
`docker cp`, which sends the same request (`PUT` at the destination's parent with the entry
renamed); that is the same chain of evidence as every other mutation. The way out was checked
against a real daemon (`TestRealDaemonCopyReadOnly`, on a symlink and a directory).

## Running everything

```sh
make test              # go test ./...            (fake daemon only)
make race              # the same under the race detector
make crosscheck        # the real docker CLI against the fake daemon
make e2e               # the scripted sessions, remote ones included; rewrites docs/screens/
make real-readonly-check   # optional: GET/HEAD-only check against your real daemon
make sandbox           # try it yourself against the fake
```
