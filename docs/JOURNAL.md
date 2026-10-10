# Build journal

Chronological record of what was built, what was checked, what was found and what changed
because of it. Decisions and their alternatives live in [DECISIONS.md](DECISIONS.md); this file
is the story of how they met reality. Newest entries are at the bottom.

Times are omitted on purpose: the order is what matters.

---

## 0. Before writing anything

**Looked at the machine.** Go 1.26, Node 24, Python 3.12 present; no Rust. `~/go/bin` is on
`PATH`. Docker Engine 29.8.2 (API 1.56) on `unix:///var/run/docker.sock`, context `default`,
containerd image store.

**Looked at what must not be touched.** The daemon holds a running container
(`optimistic_boyd`), an exited one (`minikube`) with a volume and a network of the same name,
and three images. Recorded a read-only baseline of every container (ID, state, started/finished
times, restart count), image, volume and network so that it can be diffed at the end
(see [TESTING.md](TESTING.md)).

**Two constraints discovered that shaped the plan.**

- Terminal multiplexers are not permitted in the build session and there is no interactive
  terminal. End-to-end proof therefore has to come from a scripted pseudo-terminal with a
  terminal emulator, driven from Go.
- Compound shell commands are sometimes refused by the session's permission rules, so checks
  are kept as small, single-purpose commands or as scripts in the repository.

**Process note.** The repository's OpenSpec "propose" workflow says to stop after writing the
planning artifacts and wait for a go-ahead. The request for this work said the opposite in
advance ("writing the spec is not a place to stop"). The explicit request wins; the build
continued straight after the spec validated.

## 1. Spec

Wrote `openspec/changes/docker-desk-workspace/`: proposal, eleven capability specs, design
(23 decisions), 37 tasks, each task carrying its own verification. `openspec validate --strict`
passes.

The decisions that change the shape of the thing, made at spec time and defended in the design:
official Moby client rather than the CLI or hand-rolled HTTP (D3); a fake *daemon* speaking the
real HTTP API rather than a mocked interface (D7); live data never travels through the UI
message queue (D8); a mutation is a plan built by pure code, shown, then executed verbatim
(D11); prune deletes an exact previewed set and never calls Docker's prune endpoints (D14).

## 2. Endpoint resolution

`internal/dockerctx`: `DOCKER_HOST` → `DOCKER_CONTEXT` → `currentContext` → default, plus the
unix-only rule and a classifier that turns connection failures into plain sentences. Tests use
temporary config directories and real unix sockets (missing, listening, closed, chmod 000).

## 3. Engine layer and the first contact with the real daemon

`internal/engine`: the `Engine` interface, the desk's own value types, the Moby-client
implementation, the read-only wrapper and the read-only HTTP request hook.

Ran the opt-in read-only test against the real daemon (`make real-readonly-check`). It lists
and inspects everything, tails logs, takes two stats samples and opens the event stream; the
engine is constructed read-only, and the test first proves that both layers of read-only
refuse a `DELETE` aimed at a name that cannot exist.

**Findings from real data, each of which changed the code:**

1. *An image can have no tag and still not be dangling.* With the containerd image store an
   image pulled by digest reports `RepoTags: ["kicbase/stable@sha256:…"]`, and the daemon's own
   `dangling=true` filter does not include it. The first version inferred "dangling" from
   `RepoTags`. Changed: the engine now asks the daemon (`dangling=true` list) and carries the
   verdict on each image; the list shows such a reference as repository + `<none>`.
2. *A stopped container still names its network by ID.* The exited `minikube` container lists
   network `minikube` with its `NetworkID` but no address, and `network inspect minikube` shows
   zero endpoints. That confirms both halves of D14: `docker network prune` would delete this
   network, and the conservative "referenced by any container" rule can be computed from the
   container list alone.
3. The daemon's per-image `Containers` count is populated in API 1.56 and agrees with matching
   containers by `ImageID`, which is what the planner uses.

## 4. The fake daemon, and what the real CLI taught it

`internal/fakeengine` (+ `cmd/fake-dockerd`): in-memory engine serving the real HTTP API on a
unix socket — lists, inspect, lifecycle, in-use refusals, events, multiplexed log framing with
follow and a configurable flood rate, stats, pull progress, exec with connection hijack and a
tiny scripted shell, plus a request log and fault injection for tests.

`scripts/cli-crosscheck.sh` runs the **real `docker` CLI** against the fake and asserts on the
results (61 checks: `ps`, `inspect`, `stop/start/pause/kill/rm`, `events`, `logs -f`, `stats`,
`exec`, `exec -it` under a pty, `pull`, `rmi`, volumes, networks). Every invocation carries an
explicit `-H` to a socket in a private temp directory and the script aborts before any mutation
unless the server identifies itself as the fake.

**Finding:** the first run failed four checks. Two were wrong expectations about CLI wording.
The other two were a real protocol misunderstanding in the fake: it answered an exec of a
missing executable with HTTP 400. The real daemon upgrades the connection first, writes the
error *into the stream*, closes it, and reports exit code 127 through exec inspect — which is
why the CLI said "unable to upgrade to tcp, received 400" against the fake. Fixed the fake, and
this is now a requirement on the shell session: a shell that cannot start is detected from the
exit code and the captured output, not from an HTTP error.

## 5. Live-data primitives, jobs, planner

`internal/live`: the ring buffer with stable absolute indices, the line writer (2 KiB bound,
never splits a UTF-8 sequence, lone `\r` rewinds the line for progress bars), the sanitiser,
the statistics maths, and the hub that supervises the event stream (reconnect with replay and
de-duplication), one log stream, and capped per-container statistics streams.
`BenchmarkLineWriter`: about 3 million lines per second on this machine.

`internal/jobs`: the job manager — bounded ledger, per-job output, cancellation, a panic in a
job becomes a failed job.

`internal/plan`: every rule about what a mutation may do, as pure functions over a snapshot,
plus `Execute`, the only place where a plan turns into requests. Tested against the fake for
the properties that matter: an object that became eligible after the preview is untouched; an
object that became in-use after the preview is refused by the daemon, recorded, and the rest
continue; project removal sends only container deletions; cancellation stops further steps.

A wrong expectation corrected rather than "fixed": I had assumed `UPPER/case` is an invalid
image reference. It is not — Docker's own parser reads an upper-case first component as a
registry host. The test now uses a name Docker really rejects.

## 6. The shell session

`internal/shell`: exec create → hijacked attach → raw mode → copy both ways → restore. Tested
on a real pseudo-terminal against the fake: terminal modes compared field by field before and
after; resize forwarded; Ctrl-C delivered as a byte; container dying mid-session; daemon
vanishing mid-session; shell that cannot start; and that a key typed after the session is still
there for the next reader (the keyboard reader is cancellable — a blocked `Read` left behind
would steal the first key pressed back at the desk).

## 7. The desk

`internal/ui`. First run on a pseudo-terminal drew all five panes with live data on the first
try, which mostly proves the layers underneath were sound. What the first real screens showed
that no unit test had:

- **The key hints did not fit.** Eight container actions plus navigation overflowed even at
  140 columns. Shortened the separators and labels; the full list is behind `?` and `Enter`.
- **Anonymous volumes hid their own marker.** A 64-character name pushed "(anonymous)" off the
  row. They are now shown as twelve characters, an ellipsis and the marker.
- **The exit code was the part that got cut off.** An event line read
  `container die shop-web-1 exi…`. Redesigned the line so the outcome follows the action
  (DECISIONS B5).
- **The log stream never opened when switching to the Logs tab** on an already-highlighted
  container. Stream synchronisation ran only when the *highlight* changed. It now runs on every
  frame once the highlight has settled (it is a no-op unless the wanted target changed). Found
  by the end-to-end session, which got stuck at "opening log stream…".
- **Dialog prose broke words in the middle** ("kills i / t."). Prose is now word-wrapped; log
  lines and JSON are still hard-wrapped so nothing is lost.
- **A typed confirmation looked pre-filled** because of its placeholder (DECISIONS B10).
- **A torn border** (`┘┘`) appeared in one captured screen and not the next. Chasing it found
  nothing wrong in the program: the raw output was correct. The harness was handing the
  terminal emulator chunks that ended in the middle of a three-byte box-drawing character.
  Fixed in the harness; kept the frame-geometry test it prompted, because a real tear would be
  just as easy to introduce.
- That same investigation turned up three characters (`▶ ⏸ ⚠`) that some terminals draw two
  cells wide. Replaced, and a test now scans the UI's source for any such character
  (DECISIONS B4).
- **Every end-to-end test took five seconds.** Bubble Tea was waiting for the terminal to
  answer a background-colour query that the emulator never answered (DECISIONS B11).

The safety tests were written as a table over every mutating action, with the fake daemon's
request log as the witness, and then checked for teeth by breaking the rules on purpose: with
Enter accepted as consent, and with a prefix of the confirmation word accepted, the suite
fails with "1 mutating request(s) reached the daemon". Both changes were reverted.

## 8. Responsiveness, measured

Model level, production frame interval, one container flooding its log with nine statistics
streams and the event stream running: about 300,000 lines per second ingested; 29 frames and
29 queue messages in 1.5 s; a key takes 0.7 ms (median) to 2–4 ms (worst) to take effect and
redraw. Real binary through a real pty at about 199,000 lines per second written, 196,000
ingested: a key shows on screen in 12 ms (median), 26 ms (p95).

Under the race detector, with four test binaries competing for four cores, ingestion fell to a
few thousand lines per second and the test failed its own precondition. That is the detector's
cost on a byte-level loop, not a property of the desk: the throughput precondition is asserted
without the detector and logged with it; every bound is asserted in both.

## 9. The real daemon, read-only

Ran the real binary with `--read-only` against the real engine through the pty harness: every
kind, every tab, both stream panes, help, and every mutating key pressed on the real objects —
each answered "Read-only mode… Nothing was sent to the daemon". It showed the two real
containers (live CPU and memory for the running one), three images, one volume, four networks,
and the `minikube` network with one container although that container is stopped.

Then compared the daemon with the baseline from §0: containers, container state (status,
started-at, finished-at, restart count), images, volumes and networks are all identical, and
the running container has no exec sessions.

## 10. Hardening the gaps between asking and answering

Reading the plan/dialog code once more with fresh eyes, before the independent review came
back: answers from the daemon arrive asynchronously, and three sequences were only safe by
accident (a busy flag happened to block the bad path):

- cancelling a form while its name check was in flight, then the check coming back;
- pressing Tab in a prune preview (recompute for the other scope) and typing the word before
  the new preview arrived;
- pressing an action key and then opening another overlay before the confirmation arrived.

Made them safe on purpose: every plan request carries a sequence number and only the answer to
the latest request is honoured; closing a dialog abandons its pending request; a prune preview
that is being recomputed confirms nothing until the new one is on screen; a confirmation never
replaces another dialog. `TestAnswersToAbandonedRequestsDoNothing` drives each sequence.

## 11. The independent review

Handed the mutation path to a separate reviewer (DECISIONS B30). It read the code, ran nothing
against any daemon, and came back with twelve findings and a list of what it had examined and
found sound.

The two that mattered most were both about *time*, which none of my tests had modelled:

- Planning reads the daemon first, so a confirmation opens a moment after its key. Press `t`,
  start typing `/mysql`, and the dialog could open in time for the `y` of "mysql" to stop a
  container. My own hardening in §10 had covered an answer arriving for a *closed* dialog, and
  a dialog arriving over *another dialog* — but not over the filter, which is not a dialog.
- Below 80×24 the desk drew only a message, yet every key was still routed. With the guard
  removed, the new test shows a real `POST …/stop` arriving at the fake while the screen says
  "terminal too small".

Fixed those and six more (menu target, name pinning, the in-use untag in prune-all, cancel
accounting, shell escape hatches, terminal-mode reset); kept three as they were, with wording
changes, and wrote down why. Added the corresponding scenarios to the specs so that the
contract says what the code now does. Every fix has a test that replays the reviewer's
sequence; for the three most serious, the test was confirmed to fail without the fix.

Changed my mind on one design-time decision because of this: a quarter-second arming delay on
confirmations, which I had rejected for making tests time-dependent, is now in (DECISIONS B25).

One flaky test surfaced while re-running everything: `make sandbox` returned before the fake
daemon it had started was fully gone. The recipe now waits for it.

## 12. Final state

`go vet`, `go test ./...` and `go test -race ./...` are clean; the cross-check with the real
`docker` CLI passes; the end-to-end suite was run six times in a row without a failure; the
real daemon was compared with the baseline once more at the very end (see TESTING.md). Nothing
was committed and nothing was installed outside the repository (DECISIONS B12, B13).

---

# Round two: remote daemons and themes

The request, in full: *"existing docker desk can only connect to local docker daemon. make it
can connect to remote docker daemon through ssh or any other communication protocols. document
all changes and decisions. additional task: applying theme."* Decisions are in
[DECISIONS.md](DECISIONS.md#round-two--remote-daemons-and-themes) (R1–R12, T1–T5); the spec is
`openspec/changes/remote-daemons-and-themes/`.

## 13. Before writing anything

**Read what was there.** The refusal of remote endpoints was not an omission; it was a decision
(D5) with a reason: a context left pointing at another machine is how things get deleted on the
wrong one. The request reverses it. So the first design question was not "how to connect" but
"what replaces the refusal" — answered in R1: say that the daemon is remote, in the place where
the decision to act is made.

**Looked at the machine.** OpenSSH 9.6, client *and* server binaries; the `docker` CLI; the
real daemon from round one, still not to be touched. A server binary means the SSH path does
not have to be argued for — it can be run.

**Counted connections.** The Docker CLI reaches `ssh://` by running `ssh host docker system
dial-stdio` per connection. The desk holds dozens of connections open. That arithmetic (R2)
decided the transport before any code: one tunnel, not one ssh per connection.

**Two readings of the request were written down** rather than left implicit: "any other
communication protocols" means the Docker CLI's (SSH, TCP, TLS); "applying theme" means colour
themes.

## 14. What a real sshd taught, before and during the build

A throwaway `sshd` on a loopback port, with its own host key and one throwaway client key,
forwarding to the fake daemon's socket. The first prototype was a dozen lines of shell; it
became `internal/sshtest`. Four things I believed about OpenSSH were wrong or missing:

1. **The client cannot say why the far end did not open.** A missing socket, a socket the user
   may not open, and a server that forbids forwarding all produce exactly
   `channel 1: open failed: connect failed: open failed`. The plan had been to read ssh's
   stderr for the reason. Instead the remote machine is asked (R8) — and when even that cannot
   decide, the message lists what remains.
2. **`AllowTcpForwarding no` forbids forwarding to unix sockets too.** I had restricted the
   test server with it, expecting `AllowStreamLocalForwarding local` to be what governs
   sockets; every connection was refused. Later the same happened with `PermitOpen none`. Both
   went into the launch message's hint and into the README: those are the settings a hardened
   server will have.
3. **ssh leaves its listening socket file behind when terminated.** Hence
   `StreamLocalBindUnlink=yes`, and the supervisor removing the file itself so that "no tunnel"
   reads as "no such socket" rather than "connection refused".
4. **sshd serves each connection from a child in its own session.** Killing the listener, or
   its process group, leaves established connections alive. The test helper's "the machine
   goes away" first did nothing, and then *hung*: `Wait` was waiting for a log pipe that the
   surviving child still held. The helper now ends the whole tree and logs to a file.

None of these would have been found with a stand-in ssh written from my own understanding.
The stand-in exists as well (the test binary, re-executed as "ssh") — for the supervisor's
paths, which must run everywhere — but it came second and was written to imitate what the real
one had been seen to do.

## 15. The transports

**Resolution** (`internal/dockerctx`). `--host`/`--context` with the CLI's precedence; TLS from
the environment or from the context store; endpoint parsing for the three schemes; the "how is
it reached" tag. The parsing refuses a host or user beginning with `-` — which `ssh` would read
as an option — and the destination is passed after `--` as well.

**TLS** (`internal/engine`). The client library's TLS option has no "do not verify", which the
CLI's rules need (`DOCKER_TLS`, `SkipTLSVerify`), so the configuration is built here and handed
over. Files are read at construction: a bad certificate is a launch error naming the file.

**The fake daemon on TCP and TLS** (`internal/fakeengine`). A loopback listener, and one with a
throwaway CA requiring client certificates, writing `ca.pem`/`cert.pem`/`key.pem` as Docker
names them. Everything passed on the first run — including the hijacked exec over TLS, which
the library dials by a different code path (`tls.Dial`, ignoring the transport's dialer) and
which I had expected to be the thing that broke.

**The tunnel** (`internal/sshtunnel`). Start ssh; wait for the local socket to answer (it
exists only once ssh has authenticated); supervise; restart in batch mode; clean up.

**The launch sequence** (`cmd/docker-desk`). Resolve → parse → open the way (check the socket /
dial the port / open the tunnel) → ping → info → only then the terminal.

## 16. Findings, in the order they turned up

- **The client library hides why a TCP connection failed.** `DOCKER_HOST=tcp://127.0.0.1:1`
  produced "Cannot connect to the Docker daemon at tcp://127.0.0.1:1. Is the docker daemon
  running?" — with no `ECONNREFUSED` underneath for the classifier to find. The port is now
  dialled once directly first (R8).
- **The remote tag was cut off the confirmation.** The first end-to-end run over SSH failed
  waiting for `(context default)  [ssh]`: the screen showed `…d.sock  (context def…`. The one
  thing R1 depends on had been truncated by the very endpoint it describes. Fixed in the dialog
  and, on inspection, in the header, which had the same fault in its last fall-back (R11).
- **A help line broke a word in half** ("client certif / icate") — the same lesson as round
  one's dialog prose. The three head lines of the help are now word-wrapped, and the phrases
  were shortened.
- **A data race, found by the detector in the launch tests.** ssh's stderr was still being
  passed through to the program's stderr while the program wrote its own "not reachable"
  message to it. With a real terminal that is interleaved output; with the test's buffer it is
  a race. ssh's output is now cut off from the terminal as soon as the tunnel is up — a mutex
  makes "cut off" mean that nothing is in flight — and kept for the explanation instead.
- **A message that claimed more than it knew.** When the remote probe finds the socket present
  and openable, the first wording said "the SSH server refuses to forward". A socket that
  nobody listens on — the daemon stopped, a stale file — gives the same result. The message now
  names both; a test builds a stale socket to hold it to that.
- **A test that passed without the thing it tested.** `TestKilledDeskTakesItsSSHAlong` passed
  with the parent-death signal removed. The harness starts the program as the terminal's
  session leader, so killing it hangs up the terminal, and *that* ended ssh. The test now
  starts the desk from a shell that outlives it, as in real use; with the mechanism removed it
  fails ("ssh (pid …) outlived the desk that was killed"), with it, it passes.
- **My own comment was wrong.** I had written that reverse video keeps the highlighted row
  visible under `NO_COLOR`. The test said no: without colour the renderer emits no attributes
  at all. The `▸` glyph is what marks the row, as round one designed (T5).
- **Two theme tests compared two desks and found differences that were not the theme's** —
  scroll positions, the time of day. They now redraw one unmoving desk in each theme.
- **A colour one digit off.** `#44475a` arrives at the terminal as `68;71;89`, not `…;90`:
  the colour library converts through floating point. The end-to-end assertion was loosened to
  the two leading components rather than pretend the output is something it is not.

## 17. Themes

One function builds every style from a palette of seven meanings (T1). `dark` is the original
palette; a test pins it. The named palettes were then measured for contrast against their own
schemes' backgrounds, which changed several of them (T4): Dracula's hint colour, Solarized's
green and yellow on light, four border colours, Solarized's selection background.

`T` cycles themes in the running desk. It went in after the dialog and filter routing, like
every other global key, and a test checks the three things that matter for a new key in this
program: it is a letter inside the filter, it does nothing under an overlay, and it is bound to
no action.

## 18. The independent review

As in round one (B30), the new code was handed to a separate reviewer with the rules it must
obey and no account of why I believed it did. This one ran what it suspected, with scratch
tests of its own kept outside the repository. Sixteen findings came back; the table, and what
was done about each, is in
[DECISIONS.md](DECISIONS.md#r13--an-independent-review-of-the-new-ways-in). The ones that
changed how I think about the design:

- **A prompt underneath the desk, after all.** I had a test proving that a reconnection does
  not ask for a passphrase (§16) and believed the property held. It held for *the ssh I
  started*. Reach the host through a jump host and ssh starts a second ssh, which does not
  inherit batch mode: the reviewer got a passphrase prompt written onto the desk's bottom
  line, and a `q` that no longer quit. The property I had tested was "my process does not
  ask"; the property that matters is "nothing can ask". The fix makes the second one true
  the only way it can be — no terminal to ask on — and the reviewer's scenario is now a test.
- **A path is not just a path.** I had validated the remote socket path against what I knew
  ssh's forwarding syntax does with it: split on `:`, unescape `\`. ssh also *expands* it —
  `${VAR}` from the environment, `%`-tokens — and sends the result to the server. A context
  file with the right host string would have sent an environment variable to a machine of its
  author's choosing. The first rule was a list of known dangers; the fix is a list of known
  safe characters, which does not need me to know every feature of ssh. I checked the
  expansion myself afterwards: `ssh -G -L '/l:/tmp/${X}/%i.sock'` prints the expanded path.
- **Three of Docker's rules were not what I had written down.** The default port with TLS is
  2375, not 2376; `--context default` follows `DOCKER_HOST`; TLS variables without a host mean
  `tcp://localhost:2376`. I had "followed the CLI's rules" from memory. Each was settled by
  running `docker` against a closed loopback port and reading the address in its error — the
  cheapest oracle there is, and one I should have used before writing R6.
- **The first fix for the truncated tag was the minimum that made its test pass.** A long
  context name still cut the tag; the header still cut the end of the endpoint; and with the
  extra lines, three confirmations no longer fitted 80×24 — the prompt and the input drawn
  below the last row. One of them had not fitted before round two either. Fixing it meant
  restructuring a round-one dialog, which is more than was asked for; a typed confirmation
  whose input line is off screen seemed worse than exceeding the brief.
- **Messages that said more than was known**, again (§16 had one). A daemon that accepted the
  connection and said nothing was explained as an SSH server refusing to forward. A socket in
  a directory the user may not enter was "no socket — is Docker running?".
- **Four of my tests could not fail** for the reason they existed. The Ctrl+C test passed
  without the code that ends ssh, because the terminal interrupts ssh by itself — the same
  mistake as the kill test in §16, which I had caught there and repeated here.

Two points were kept as they were, and say so in the README: a loopback address is not called
remote, and through a proxy the shell's connection goes direct.

The fixes were then themselves checked the way the original rules were: for four of them the
test was run with the fix removed and seen to fail. They were not sent back for a second
review; TESTING.md says so.

A note on working conditions: the reviewer and I were working in the same directory at the
same time. Where its line numbers and mine disagreed, its report said which state of the tree
it had read.

## 19. Final state

`go vet`, `go test ./...` (399 tests and subtests, none skipped but the two opt-in ones) and
`go test -race ./...` are clean; `make crosscheck` passes its 61 assertions; both OpenSpec
changes validate. Screens 19 to 23 are new; 12 was re-captured, because the committed one
turned out to have been taken in the middle of a frame (the capture now waits for the screen
to hold still). The other captures were left as they were: re-taking them changes only
temporary paths and CPU figures.

See [TESTING.md](TESTING.md) for what is proven and what is not. As in round one, nothing was
committed and nothing was installed outside the repository. The real daemon on this machine
was not sent a single request by any of this work: every run of the binary named the fake with
`--host` or `DOCKER_HOST`, and the `docker` CLI was only ever pointed at the fake, at a
context store in a temporary directory, or at a closed port.
