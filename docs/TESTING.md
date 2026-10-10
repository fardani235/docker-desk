# How Docker Desk is proven — without touching your containers

The constraint was: prove that a tool which stops, kills and deletes things works, **without
ever mutating the containers that actually exist on this machine**. This file says how that
was done, what was measured, how to repeat it, and — just as important — what is *not* proven.

## The short version

| What | With | Real daemon involved? |
| --- | --- | --- |
| Every rule about what may be changed, and how | pure planner tests | no |
| Every request the desk can send | Docker's own client library against a fake daemon | no |
| The fake daemon itself | the real `docker` CLI driving it, 61 assertions | no |
| Every key, pane and dialog | the UI model driven key by key; the fake's request log is the witness | no |
| Responsiveness under a log flood | model-level and real-binary measurements | no |
| The real program, end to end, including the shell hand-off | the binary on a pseudo-terminal with a terminal emulator | no |
| That it reads a real engine correctly | the engine layer, then the whole program, in read-only mode | **yes — `GET`/`HEAD` only, enforced** |
| That nothing on the real engine changed | before/after comparison of every container, image, volume and network | read-only |
| Remote daemons over TCP and TLS (round two) | the client library and the real binary against the fake daemon on a loopback port, with real certificates | no |
| Remote daemons over SSH (round two) | the real `ssh` client through a real `sshd` on a loopback port to the fake daemon's socket | no |
| The Docker context store and TLS rules (round two) | a context written and used by the real `docker` CLI, then read by the desk | no |
| Themes (round two) | every frame compared across themes; contrast computed; the real binary's colour output | no |

`go test ./...` runs 399 tests and subtests — 198 tests, the rest their sub-cases. (Round one
ended at 194 in all.) Two are opt-in and skipped by default. Twelve tests need a real `sshd`
and skip themselves on a machine without OpenSSH's server; on this one none was skipped, and
`DOCKER_DESK_REQUIRE_SSHD=1` turns such a skip into a failure, so that a helper that quietly
stopped working cannot hide behind a green run. Everything passes with and without the race
detector.

The remote transports and the themes have [their own section](#round-two-remote-daemons-and-themes)
below.

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

## What is not proven

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
- **Event replay after a reconnect** was tested against the fake's event history; the real
  daemon keeps a similar bounded history, but its behaviour across a daemon *restart* (history
  lost) relies on the full refresh that follows every reconnect.

## Running everything

```sh
make test              # go test ./...            (fake daemon only)
make race              # the same under the race detector
make crosscheck        # the real docker CLI against the fake daemon
make e2e               # the scripted sessions, remote ones included; rewrites docs/screens/
make real-readonly-check   # optional: GET/HEAD-only check against your real daemon
make sandbox           # try it yourself against the fake
```
