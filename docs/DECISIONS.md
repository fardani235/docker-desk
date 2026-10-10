# Decisions

Why Docker Desk is built the way it is. Every entry says what was decided, what else was on the
table, and what tipped it. Entries marked **D** were made when the spec was written (they are
also in `openspec/changes/docker-desk-workspace/design.md`); entries marked **B** were made
during the build, usually because something real contradicted an assumption — the
[journal](JOURNAL.md) tells those stories in order.

Where a decision costs something, the cost is stated. Nothing here is presented as free.

---

## Shape of the product

### D1 — Go, shipped as one binary

**Decided:** write it in Go; ship a single self-contained executable.

**Alternatives:** Python + Textual (excellent widgets, but needs an interpreter environment at
run time and a GIL-bound event loop is the wrong place to absorb a 50,000-lines/s log flood);
Rust + ratatui (no toolchain on the machine, no first-party Docker client); Node + Ink (a
React reconciliation per frame is a poor fit for high-rate streams).

**Why:** Go was already installed, `~/go/bin` was already on `PATH` (so `go install` yields a
command you can type), goroutines map one-to-one onto "many independent live streams", and it
is the language of Docker's own client library (see D3).

### D2 — Bubble Tea v1 and Lip Gloss, panes written by hand

**Decided:** the UI is a Bubble Tea model: `Update(message) → state`, `View() → text`. Tables,
scrolling text, tabs and dialogs are written here; only the single-line text input comes from
the `bubbles` component set.

**Alternatives:** tview/tcell (richer widgets, but widgets own mutable state and draw from any
goroutine, which makes both frame coalescing and deterministic tests harder); Bubble Tea v2
(newer API; v1 is the long-stable line and has everything needed).

**Why:** a UI that is a plain state machine can have its safety rules tested as functions —
"after this key, zero mutating requests" — without a terminal. Bubble Tea also has a
first-class "give the terminal to something else and take it back" primitive, which the shell
needs (D16). Hand-written panes because every pane must draw from a shared ring buffer in
O(visible rows) and must be able to say "nothing changed" cheaply (D8).

**Cost:** more UI code to own (about 4,000 lines), and one import-time side effect of Bubble
Tea that cannot be turned off (B11).

### D5 — Unix sockets only; anything else is refused at launch

**Decided:** if the current context resolves to `tcp://`, `ssh://`, `npipe://` or anything
that is not `unix://`, the program prints the endpoint, says why, and exits without opening
the workspace.

**Alternative:** let the client library connect to whatever the context says.

**Why:** "no connecting over the network" was a stated boundary, and a context left pointing
at a remote host is one of the likeliest ways to delete things on the wrong machine. Refusing
is cheaper than being careful.

### D18 — Layout: act on the left, the engine answers on the right

**Decided:** five panes always on screen. Wide terminals (≥ 120 columns): Kinds on the left,
List over Details in the centre, Events over Jobs on the right — Jobs in the bottom corner.
Between 80 and 119 columns the right column becomes a strip under the centre so all five stay
visible. Below 80×24 a message states the minimum size.

**Why:** the request described a rhythm — "I act on one side, the engine answers on the
other" and progress "in a corner" — and the layout is that sentence drawn.

**Focus** is shown three ways at once: heavy border, accent colour, and a `◆` marker in the
title. The first and third survive `NO_COLOR`.

### D21 — Keys

Lower-case letters for everyday reversible actions (`s` start, `t` stop, `p` pause, `u`
unpause, `e` shell, `n` new, `c` connect), and deliberately awkward ones where a slip would
cost more (`K` kill, `X` prune, `D` disconnect, `R` restart, `P` pull). `d` is "remove" in
every kind. None of them acts without its confirmation, so the awkwardness is a second line of
defence, not the first. The full list is in the README and behind `?`.

### B7 — Actions also work from the Details pane

**Decided:** with the Details pane focused, the highlighted object's action keys still work.

**Alternative:** actions only from the List pane, so that "the pane I'm acting on" is always
the list.

**Why:** the common loop is "read the logs, restart it, keep reading". The Details pane's
title names the object, none of its own keys (`j k g G f w h l`) collide with an action key,
and every action still opens a dialog that names the object. Forcing a focus change first
would add a keystroke and no safety.

### B6 — The filter filters as you type

**Decided:** `/` filters rows on every keystroke; Enter keeps the filter, Esc restores what
was there before; Esc in the list clears it.

**Why:** a filter you have to submit before seeing its effect is a search box. Keys typed into
the filter are text and can never reach an action (tested).

### B22 — Rows are ordered so they hold still

**Decided:** containers: running/paused first, then by name. Images: by repository and tag,
untagged last. Volumes: named first, then anonymous. Networks and projects: by name.

**Alternative:** Docker's order (newest first).

**Why:** on a desk that refreshes by itself, a row that jumps is a row you act on by mistake.
The highlight follows the object's identity regardless (it is tested), but stable order means
the eye does too.

### B23 — The Info tab does not print environment variable values

**Decided:** the container summary shows how many environment variables there are, not their
values. The Inspect tab shows the daemon's full document, values included.

**Why:** environment variables are where credentials live, and a desk is often on a shared or
recorded screen. One deliberate tab switch is a reasonable price for seeing them.

---

## Talking to Docker

### D3 — Docker's own client library, not the CLI and not hand-rolled HTTP

**Decided:** every request goes through `github.com/moby/moby/client`, the current home of
Docker's Go client (its API module is 1.56, the same as the daemon here), with API-version
negotiation.

**Alternatives:**

- *Shell out to `docker …`.* Simplest. But it adds a run-time dependency on a binary, forces
  parsing human-oriented output or spawning a process per call, gives no structured errors
  (409 "in use" versus 404 "gone"), and makes many concurrent streams expensive.
- *Hand-roll HTTP over the socket.* Smallest dependency tree and full control.

**Why — the deciding argument is about what cannot be tested.** Mutating requests may never be
sent to the real daemon during development. If the request encoding were written by hand, a
fake daemon written by the same hand would faithfully mirror any misunderstanding, and the
tests would pass. With the library the Docker CLI itself uses, the encoding of every stop,
remove and exec is Docker's.

**Cost:** a larger dependency tree (OpenTelemetry arrives transitively) and a binary of about
9.5 MB.

### D4 — Context resolution re-implemented (about 100 lines) instead of importing `docker/cli`

**Decided:** `internal/dockerctx` applies the CLI's precedence itself: `DOCKER_HOST` →
`DOCKER_CONTEXT` → `currentContext` in `$DOCKER_CONFIG/config.json` → the default socket; a
named context's endpoint is read from `contexts/meta/<sha256(name)>/meta.json`.

**Alternative:** import the CLI's context store.

**Why:** the client library knows nothing about contexts, and the CLI's store would bring most
of `docker/cli` with it. The rules are small and stable. Doing it here also lets the resolver
return *where the endpoint came from*, which the header, `--check`, the help overlay and the
launch errors all print.

**Cost:** if Docker ever changes how contexts are stored, this must follow.

### D6 — A narrow `Engine` interface with the desk's own types; no `force` anywhere

**Decided:** the rest of the program depends on one interface returning small value types
(`Container`, `Image`, `Volume`, `Network`, `Event`, …). Every mutating method takes the
identifier of exactly one object. There is no force parameter, no cascade flag, no "all".

**Why:** library churn stays in one file; read-only mode becomes a wrapper (D17); and a forced
removal cannot be requested by mistake because it cannot be expressed. `TestNeverForces` reads
what actually went over the wire to confirm it.

### B1 — "Dangling" is the daemon's verdict, not a guess

**Found:** on this machine (containerd image store) an image pulled by digest reports
`RepoTags: ["kicbase/stable@sha256:…"]` — no tag — and yet the daemon's own `dangling=true`
filter does not include it.

**Decided:** the engine asks the daemon which images are dangling (a second, filtered list
call) and carries that flag on each image. A digest-only reference is displayed as repository
+ `<none>` and is removable by that reference.

**Alternative:** infer "dangling" from an empty `RepoTags`. That is what the first version did;
it would have offered an image for pruning that Docker does not consider dangling.

**Cost:** one extra request per image refresh.

### B18 — HTTP 403 from the daemon is treated as a refusal, like 409

**Found:** Docker answers most "not in that state" refusals with 409 but network ones
("has active endpoints", "is a pre-defined network") with 403.

**Decided:** both are classified as "refused because of the object's state". The message shown
is always the daemon's own sentence.

---

## Making it safe

### D11 — A mutation is a *plan* built by pure code, then shown, then executed verbatim

**Decided:** `internal/plan` turns (action, target identifier, snapshot of the engine) into a
`Plan` value: title, exact target, effect sentences, the list of steps, blockers, and how it
must be confirmed. The dialog renders that value. The job executes that same value.

**Alternative:** the usual shape — the key handler checks a few things and calls the API after
a yes/no prompt.

**Why:** with one value shown and executed there is no second lookup between seeing and doing.
"Confirm the exact object" and "a prune destroys only what it showed" stop being things to be
careful about and become things that are true by construction. And because planning is pure,
every rule is tested exhaustively without a UI or a daemon.

### B14 — Plans are built from a fresh snapshot, not from the lists on screen

**Decided:** pressing an action key captures the highlighted object's identifier, then reads
the four lists from the daemon again and plans from that. While that read is in flight further
action keys are ignored.

**Alternative:** plan from the data already on screen (instant, no request).

**Why:** the screen can be a few hundred milliseconds — or, when disconnected, minutes — out
of date, and "is it in use?" is exactly the kind of fact that goes stale. The cost is a few
milliseconds and four `GET`s per action. When the daemon cannot be read, no dialog opens at
all; the desk says so.

### D12 — Three ways to confirm, scaled to what cannot be undone

| Tier | Actions | To proceed |
| --- | --- | --- |
| confirm | start, stop, restart, pause, unpause, kill, connect, disconnect, stop project | press `y`. Enter does nothing; `n` or Esc cancels |
| typed | remove container / image / volume / network, remove project, every prune | type the word shown (`remove` or `prune`), then Enter |
| form | pull image, create volume, create network, open shell | type (or accept) the value, then Enter |

**Alternatives considered:**

- *Type the object's name* (the GitHub way). Strongest — but dangling images have no name and
  anonymous volumes are 64 hex characters. A control that is unbearable gets resented and
  worked around. A fixed word still cannot be produced by a stray key or key-repeat, and it is
  the dialog — not the typing — that carries the object's identity.
- *`y` for everything.* Then `d`,`y` becomes one muscle-memory gesture for deletion.
- *Enter confirms, with "Cancel" focused by default.* One mis-aimed Tab away from consent.

**Cost:** friction on a frequent action (removing a container is seven keys and Enter). That is
the intended trade.

All three tiers also require that the input arrives *after* the dialog could be read — see
B25 and B26, added after the review.

### B10 — A typed confirmation shows no placeholder

**Found** while reading a captured screen: the input's dim placeholder showed the word to
type, which in a plain-text capture — and at a glance in a terminal — looked as if it had
already been typed.

**Decided:** the input starts empty. Nothing in a confirmation may look pre-filled.

### D13 — Nothing is forced; "in use" is decided twice

**Decided:** in-use is checked when the plan is built — a blocker makes the dialog a refusal
with no way to proceed — and again by the daemon, because every delete is sent without force
and Docker answers "in use" itself. `kill` is the one deliberately forceful action, and it is
an explicit request.

**Why twice:** the plan's check gives a refusal that names what is using the object before
anything is sent; the daemon's check covers the gap between the dialog and the request.

### D14 — A prune deletes an exact set; Docker's prune endpoints are never called

**Decided:** the plan computes the candidates, the dialog lists every one, and the job deletes
exactly those identifiers, one non-forced request each.

**Alternative:** preview with list calls, then call `/…/prune`.

**Why:** Docker's prune has no dry run and decides what to delete at the moment it runs. A
preview followed by `/prune` can destroy things that became eligible after the preview. Here,
something created after the preview is simply not in the set (tested), and something that
became in-use is refused by the daemon and reported per object while the rest continue
(tested).

**"Unused" is also defined more conservatively than Docker's.** A volume or network referenced
by any container — *including stopped ones* — is in use. On the machine this was built on,
that is the difference between keeping and deleting the `minikube` network, which only an
exited container references and which `docker network prune` would remove.

**Cost:** N requests instead of one; no reclaimed-space figure for volumes; and the desk
prunes less than `docker … prune` does. The dialog says so.

### B17 — Pruning a tagged image is one step per reference

**Decided:** in "all unused" scope, an unused image with several tags appears as one row per
tag, each an untag; the last one deletes the image.

**Alternative:** one row per image, deleted by ID.

**Why:** deleting a multi-tag image by ID requires force ("referenced in multiple
repositories"), and nothing is forced. It also keeps the preview literal: every row is one
request.

### B16 — Untagging one of several references is allowed even when the image is in use

**Decided:** if an image has other references, removing one is only an untag; it is not
blocked by containers using the image, and the dialog is titled "Untag image" and says the
data stays.

**Why:** that is what Docker does, nothing is lost, and refusing would make a harmless tidy-up
impossible while a container runs.

### D15 — Compose is a label, and "remove project" removes containers only

**Decided:** a project is the set of containers carrying the same
`com.docker.compose.project` label. Stop stops the running ones. Remove removes the containers
— refused while any is running — and nothing else. Networks and volumes carrying the project's
label are *named* in the confirmation and left in place.

**Alternative:** also remove the project's networks, which is what `compose down` does.

**Why:** the request was explicit — "no magic over compose files… no down". A volume is data;
a network removal is exactly the `down` that was not wanted. They stay visible under their own
kinds, where they can be removed or pruned deliberately.

### D17 — Read-only mode is enforced below the UI, twice

**Decided:** `--read-only` (a) wraps the engine so every mutating method, and exec, returns
`ErrReadOnly` without calling the client, and (b) installs a hook on the HTTP client that
rejects any request that is not `GET` or `HEAD` before it is sent.

**Why two layers:** the wrapper is the designed path; the hook means a future code path that
forgets the wrapper still cannot mutate. A reflection test fails if a new mutating method is
added to the interface without being covered. This is what made it safe to run the real
program against the real daemon during development.

### B25 — A confirmation ignores what proceeds for its first quarter of a second

**Found** by the independent review (B30): the confirmation rules said *which* key consents but
not *when*. `t` followed at once by `y` — muscle memory, or keys typed ahead while the machine
was busy — consented to a dialog nobody had read.

**Decided:** a confirmation, the container picker and the shell form (whose command is
pre-filled) ignore the input that proceeds — `y`, text for the typed word, Enter — until they
have been open for 250 ms. Esc is never delayed.

**Alternative:** no delay. That is what the design chose at first, because time-dependent
behaviour makes tests awkward. The review changed the weighing: without it "stop and make me
confirm" could be satisfied by a two-key reflex.

**Cost:** a key pressed too early is silently ignored and has to be pressed again; tests carry
a knob to shorten or disable the delay, and the end-to-end tests wait as a person would.

### B26 — Any key pressed while a confirmation is being prepared drops it

**Found** by the review, and the most serious finding: planning reads the daemon first (B14),
so a dialog opens a moment after its action key. If the operator pressed `t` and then started
typing a filter — say `/mysql` — the dialog could open under their fingers and the `y` of
"mysql" would stop a container.

**Decided:** a pending confirmation exists only as long as the operator does nothing else. Any
key pressed before it appears drops the request (the status line says so), and the key is then
handled normally. In addition a confirmation never replaces another overlay, an answer to
anything but the latest request is discarded, and closing a dialog abandons whatever was still
being computed for it. While a request is pending the status line says a confirmation is about
to open, and that takes precedence over any other message.

**Alternative:** block all keys while a plan is being prepared. Simpler, but it freezes the
desk for as long as a slow daemon takes to answer, which is exactly when the operator most
wants to do something else.

### B27 — Nothing can be acted on while the desk cannot be drawn

**Found** by the review: below 80×24 the desk shows only "terminal too small", but keys were
still routed. `t`, `y` stopped the highlighted container with nothing on screen, and a dialog
that was open when the window shrank could be confirmed blind. Removing the fix makes the test
fail with a real `POST …/stop` in the fake's log.

**Decided:** while the terminal is too small (or its size is not yet known) only quitting is
accepted. An open dialog stays open and reappears, unconfirmed, when the window grows back.

### B28 — Steps addressed by a name are pinned to the object that was shown

**Found** by the review: containers and networks are addressed by immutable IDs, but an image
reference and a volume name are *names*. Between the dialog and the request a tag can be moved
to another image by a rebuild, or a volume removed and recreated — and the daemon would then
act, without complaint, on an object nobody was shown.

**Decided:** such steps carry a pin — the image ID the reference pointed at, or the volume's
creation time — and `Execute` re-reads it immediately before the request, refusing if it
changed. Two related promises are checked at the same moment: an *untag* confirmed as "the
image data stays" is refused if the reference has become the image's last one, and a prune
step on a tagged image is refused if a container now uses the image (the daemon refuses to
*delete* an in-use image but not to untag it, so "left alone" has to be enforced here).

**Limits, stated:** the check and the request are two calls, so the window shrinks from
"however long the dialog was open" to a few milliseconds; it does not close. A volume's
creation time has one-second resolution. Neither can be fixed from the client: the Engine API
has no "delete this reference only if it still points at X".

**Cost:** one or two extra `GET`s per such step.

### B8 — Cancelling a job needs no confirmation

**Decided:** `c` on a running job cancels it at once.

**Why:** the rule is that no single key *changes or deletes* anything. Cancelling only ever
stops further requests from being sent; it cannot make the daemon do something. What had
already been done is listed in the job's output.

### B19 — Ctrl+C is never consent

**Decided:** with jobs running, `q` or Ctrl+C opens a dialog that names them; only `y` quits.
Pressing Ctrl+C again does nothing.

**Why:** Ctrl+C is the key people hammer. Quitting cancels running jobs, which for a
half-finished multi-object job is a decision, not a reflex.

---

## Staying responsive

### D8 — Live data never travels through the UI's message queue

**Decided:** each stream (logs, each container's statistics, events, job progress) is read by
its own goroutine into a fixed-capacity ring buffer that drops its oldest entry, and bumps an
atomic version counter. Nothing is sent to the UI. The UI has one 50 ms frame tick; on a tick
it compares version counters with the ones it last drew and, if none moved, reuses the cached
frame. Drawing a pane copies only the rows that will be visible.

**Alternatives:**

- *A message per line, perhaps batched.* Key latency stays coupled to stream volume through
  the shared queue, and batch windows become one more thing to tune.
- *Back-pressure the daemon* (stop reading when behind). A follow view wants the *latest*
  output; blocking the reader makes the daemon buffer and the view show stale lines.

**Why:** key presses are then the only traffic on a queue that streams never write to, so
their latency is one `Update` and one `View`, whatever the streams are doing. Measured: with
300,000 log lines per second arriving, 29 frames and 29 queue messages in 1.5 s, and a key
takes about a millisecond to take effect ([TESTING.md](TESTING.md)).

**Bounds:** 5,000 log lines; 2,048 bytes per line (longer output is split, never cutting a
character in half); 1,000 events; 120 statistics samples per container; 50 jobs with 500 output
lines each.

**Cost:** under a flood the log view is a window onto the newest 5,000 lines, not a complete
record. The view says how many older lines have scrolled out.

### D9 — Lists refresh on engine events (rate-limited) plus a slow poll

**Decided:** an engine event marks the kinds it affects; each list is refreshed at most once
per 150 ms and never while a refresh of it is in flight; a 5 s poll covers what events do not
report (the "Up 3 minutes" text, anything missed across a reconnect). Responses carry a
sequence number so a slow old one cannot overwrite a newer one.

**Measured:** 101 events in a burst cause one or two container-list requests.

### D10 — Every mutation is a job, even a fast one

**Decided:** a job is a function with a context and a reporter; the manager keeps a bounded
ledger. `start` on a container is a job just as a pull is.

**Why:** one code path, one place errors land, nothing that can block the desk — and the Jobs
pane becomes a record of everything the desk asked the daemon to do, with the daemon's answer.
A panic inside a job is caught and becomes a failed job rather than a broken terminal.

### D19 / B9 — Statistics for every running container, capped at 32 streams

**Decided:** one statistics stream per running container feeds the CPU and MEM columns and the
Stats tab. At most 32 streams are open; the highlighted container is always one of them; rows
beyond the cap show `n/c`. Only *running* containers are streamed.

**Why:** "is it actually dying" is a question about all containers at once, which is what
`docker stats` answers. The cap keeps a host with hundreds of containers from opening hundreds
of streams.

### D20 — Container output is sanitised before it is drawn

**Decided:** everything except colour (SGR) sequences is removed from log lines: cursor
movement, screen clears, alternate-screen switches, window-title and clipboard (OSC)
sequences, terminal resets, C1 controls. Tabs are expanded, invalid UTF-8 replaced. With
`NO_COLOR`, colour is stripped too.

**Why:** container output is untrusted bytes written to the operator's terminal. Without this
a container could repaint other panes, retitle the window or write to the clipboard.

### B21 — A log stream that ended is reopened when its container starts again

**Found:** the daemon closes a log follow when the container exits.

**Decided:** the view keeps the last output and says the container is not running; when the
engine reports that container starting, the stream is reopened with a fresh tail. Reopening
replaces the buffer rather than appending, so no line appears twice.

### B5 — An event line puts the outcome right after the action

**Found** on the first real screen: `container die shop-web-1 exi…` — the exit code, the one
thing you want from a `die` event, was what the narrow pane cut off.

**Decided:** `15:04:05 ctr die exit 137 web`: time, a three-letter kind, the action with its
outcome (exit code, signal), then the name. Network events show the container's *name*
(resolved from the list), not its ID.

**Cost:** `ctr`/`img`/`vol`/`net` instead of the full words.

### B4 — No character that some terminals draw two cells wide

**Found:** `▶`, `⏸` and `⚠` have an emoji presentation; some terminals draw them double-width
whatever the width tables say, which tears every border to their right.

**Decided:** they were replaced (`◆`, `‖`, `▲`), and a test scans every non-ASCII character in
the UI's source and rejects any that is not one cell wide or lies in an emoji-presentation
range. A second test checks that every line of every frame, at nine terminal sizes, every
kind, tab and focus, is exactly the terminal's width.

---

## The shell

### D16 — A native exec session handed the terminal through `tea.Exec`

**Decided:** the session is implemented against the API — exec create, hijacked attach,
resize — and wrapped as a `tea.ExecCommand`. Bubble Tea leaves the alternate screen; the
session puts the terminal into raw mode, copies bytes both ways and forwards `SIGWINCH`; on
every exit path the terminal state is restored before Bubble Tea re-enters the desk.

**Alternative:** spawn `docker exec -it`. A few lines — but it adds a run-time dependency on
the CLI and a second, independent resolution of "which daemon" that could disagree with the
header.

**Details that matter:**

- The keyboard reader is *cancellable*. A goroutine left blocked reading the terminal would
  swallow the first key pressed back at the desk; the pseudo-terminal test proves the next
  reader gets the key.
- The default command is `sh -c 'command -v bash >/dev/null 2>&1 && exec bash || exec sh'`,
  shown in a form and editable before anything runs.
- Opening a shell is disabled in read-only mode: it runs a process inside a container.

### B29 — The shell session can always be left

**Found** by the review: while the terminal is handed over, Bubble Tea ignores signals and its
event loop is blocked inside the session. A daemon that accepted the exec request and never
answered would hang the program with no key that helps; a daemon that went silent mid-session
(connection open, no data) would be indistinguishable from a quiet shell.

**Decided:**

- starting is bounded: 15 s for the daemon to create and attach, then the desk returns with a
  message;
- **Ctrl-] three times** ends the session locally, without the daemon's cooperation (the
  banner says so; one or two are ordinary bytes for the remote program);
- `SIGTERM` and `SIGHUP` end the session cleanly instead of being lost, so that killing it
  from another terminal does not leave the tty raw;
- when the session ends, however it ends, mouse reporting, bracketed paste, application cursor
  keys, the alternate screen and a hidden cursor are switched off — a full-screen program
  inside a container that dies has no chance to clean up after itself;
- keys typed blind while the session was ending are discarded rather than delivered to the
  desk (Linux).

### B2 — "The shell could not start" is detected from the stream and the exit code

**Found** by running the real `docker` CLI against the fake daemon: the fake answered an exec
of a missing executable with HTTP 400, and the CLI said "unable to upgrade to tcp, received
400". The real daemon upgrades the connection *first*, writes the error into the stream,
closes it, and reports exit code 126/127 through exec inspect.

**Decided:** the fake was corrected, and the session keeps the last 2 KiB of output; a session
that ends within two seconds with exit code 126 or 127 is reported as "could not start"
together with the daemon's own sentence from the stream.

---

## Proving it without touching anything real

### D7 — The test double is a fake *daemon*, not a fake interface

**Decided:** `internal/fakeengine` is an in-memory Docker Engine serving the real HTTP API on
a unix socket: lists, inspect, lifecycle, in-use refusals with Docker's status codes and
wording, events, the multiplexed log framing with follow and a configurable flood rate,
statistics, pull progress, exec with connection hijacking and a small scripted shell. Tests
point the real client library at it. It records every request, which is how tests assert
"nothing was sent".

**It is cross-checked from both directions**, because a fake written next to the code can agree
with the code and still be wrong:

1. the real `docker` CLI is run against the fake (`make crosscheck`, 61 assertions), so the
   fake's behaviour is validated by Docker's own client;
2. the desk's engine layer — and then the whole program, read-only — is run against the real
   daemon, so its decoding is validated by Docker's own server.

**Alternatives:**

- *Mock the `Engine` interface.* Far less code; leaves the client-library integration, the
  stream framing and the exec hijack untested.
- *Throwaway containers on the real daemon.* Closest to reality — but it creates objects on a
  machine holding things that matter, needs an image pull, and one bug in test clean-up
  becomes a real deletion. Rejected: the real daemon received no mutating request at all.
- *Docker-in-Docker.* Needs a privileged container on the same daemon; same objection.

**Cost, stated plainly:** the mutating paths had never run against a real daemon before the
first real use. What stands in for that is the chain above — Docker's client encodes every
request, and Docker's CLI validates the fake those requests were tested against.

### B20 — The sandbox is a separate program, not a `--demo` flag

**Decided:** the fake daemon ships as `fake-dockerd`, and `make sandbox` starts it and points
the desk at it through `DOCKER_HOST`.

**Alternative:** `docker-desk --demo`.

**Why:** the product talks to exactly one daemon chosen by the Docker context, and should have
no second way to choose. In the sandbox the header shows the sandbox socket like any other
endpoint — the same mechanism you rely on to know your blast radius.

### B3 — Tests cannot open a read-write connection to a real daemon

**Decided:** every test package that builds an engine calls `engine.InstallTestGuard()` from
`TestMain`. From then on `engine.New` refuses a read-write connection to any socket outside
the system temporary directory, and `DOCKER_HOST` points at a path that cannot exist.

**Why:** "the tests only use the fake" was true by convention. This makes it true by
construction: the two opt-in tests that visit a real daemon can only open it read-only.

### B11 — The pseudo-terminal harness answers the questions a real terminal answers

**Found:** every end-to-end test took five seconds to start. Bubble Tea asks the terminal for
its background colour at import time and waits up to five seconds for a reply; the emulator
used by the harness never replied.

**Decided:** the harness answers the background-colour and cursor-position queries, as real
terminals do. (A second harness bug found the same day: a read from the pseudo-terminal can end
in the middle of a multi-byte character; the harness now only hands the emulator whole
characters. Before that, borders occasionally appeared torn in captured screens when nothing
was wrong with the program.)

**What it means for you:** in tmux and screen the query is skipped; in any terminal that
answers cursor-position reports — all the usual ones — there is no wait. A terminal that
answers neither would pause for five seconds at start-up. `NO_COLOR=1` also skips it.

### D22 — What counts as proof

Six layers, each catching what the one before cannot: unit tests of the pure parts; the real
client library against the fake daemon; the UI model driven key by key with assertions on the
fake's request log; a flood test for the responsiveness bounds; the real binary on a
pseudo-terminal with a terminal emulator; and the real binary, read-only, against the real
daemon with a before/after comparison of everything on it. Details and numbers:
[TESTING.md](TESTING.md).

---

### B30 — An independent review of the mutation path, before calling it done

**Decided:** after the tests were green, the code between a key press and a mutating request
(`plan`, the UI's keys/dialogs/actions, `engine`, `jobs`, `shell`) was handed to a separate
reviewer with the rules it is supposed to obey and no account of why I believed it did. It was
asked for concrete sequences that break a rule, not for opinions.

**Why:** every test until then had been written by the author of the code, against the
author's idea of how it could fail. For a tool that deletes things that is not enough.

**What came back, and what was done:**

| Finding | Outcome |
| --- | --- |
| A confirmation that opens late can be confirmed by keys typed for something else | fixed — B26, B25 |
| Dialogs are live but not drawn in a too-small terminal | fixed — B27 |
| The action menu names one object and acts on the highlight | fixed: the menu acts on the row it captured, or reports it gone |
| A recomputed prune preview appears after the operator moved on | fixed — B26 |
| Image and volume steps addressed by name are not pinned | fixed — B28 |
| Prune-all can strip a tag from an image that became in use | fixed — B28 |
| Cancelling relabels a job that failed by itself; failures before a cancel are lost | fixed: only a job that stopped *because* it was cancelled is "cancelled"; the output counts succeeded, failed and not attempted |
| A stalled daemon during a shell leaves no way out; terminal modes left on | fixed — B29 |
| The shell form is pre-filled, so `e`, Enter runs it without typing | kept: the spec asks for the command to be shown and editable, and a shell deletes nothing — but Enter is now subject to B25 |
| Untagging an in-use image is not blocked | kept (B16) — the dialog now says which containers keep using the image |
| A prune also removes untagged parent layers that are not listed | kept: that is what removing an image means; the dialog now says so instead of "exactly the images listed" |

Each fix has a test that reproduces the reviewer's sequence, and for the three most serious
the test was confirmed to fail when the fix is taken out.

## Process

### B12 — No commits were made

The work was done on the branch that was checked out, and nothing was committed: committing
was not asked for. Everything is in the working tree for review.

### B13 — The binary was not installed into `PATH`

`make build` leaves `bin/docker-desk`; `make install` puts it in `~/go/bin`, which is already
on `PATH`. Writing outside the repository was left as a one-command decision for the owner of
the machine. (`go install` into a scratch directory was verified to work.)

### B24 — The OpenSpec change is complete but not archived

All tasks are done and the change validates. Archiving — which folds the capability specs into
`openspec/specs/` — is an acceptance step and was left for after review (`/opsx:archive`).
