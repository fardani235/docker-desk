# Design

## Context

See `proposal.md` for the motivation and `specs/image-copy/spec.md` for the behavior contract.

- `internal/engine.Engine` is one daemon's whole API. `SaveImage(ctx, id, platform)` returns
  the `GET /images/get` stream; there is no load method. The moby client already offers
  `ImageLoad`, which posts a raw `application/x-tar` body to `POST /images/load` and returns a
  JSON progress stream as a plain `io.ReadCloser` — it has no message iterator, unlike
  `ImagePullResult`.
- A `plan.Plan` is pure data rendered by the confirmation dialog and executed by
  `plan.Execute(ctx, eng, p, run)`, which takes exactly one engine. Push is already a
  bespoke branch (`executePush`) because its request carries a credential and its own pin.
- `internal/session` holds every connected member with its own engine, endpoint, link and
  hub; exactly one member is focused. Background members keep only an event stream and a ping.
- `internal/jobs.Manager` records one `Daemon` per job; `Session.Remove` consults
  `Jobs.CloseDaemon` under the job manager's lock, which is what makes "a daemon with a running
  job is not removed" hold.
- `internal/ui` has the precedents this change follows: `w` save archive (bound to one image,
  platform, pin, one job), `p` push (typed confirmation, outward effect), `t`/`u` (local
  references), and the generic `dlgPicker` used to choose a container.
- The real archive from `SaveImage` is a legacy `docker save` tar: `manifest.json` with
  `Config` (`<hex>.json`), `RepoTags` and `Layers`, plus the config JSON whose `rootfs.diff_ids`
  identifies the build's layers. The fake daemon's fixtures are built the same way.

## Goals / Non-Goals

**Goals**

- One deliberate action moves one image's shown platform from the focused daemon to another
  connected daemon, in one job, with no file on this machine and no registry request.
- The confirmation states the exact source, target, tag effect and platform before anything is
  streamed, and the result is verified against the source's content.
- Neither daemon's other state changes; the source is only read.
- Both daemons stay protected from removal while the stream runs.

**Non-Goals**

- Volumes, containers, compose projects, or any object other than one image.
- Multi-platform transfer, retagging as part of the copy, `--all-tags`, or copying to a context
  that is not already a session member.
- Resumable or retryable streams, staging files, or a transfer daemon.
- Acting on a selection; the action is one row at a time.
- Changing the source in any way, including "moving" the tag off it.

## Decisions

### D1 — Stream directly from source to target; never stage a file

The job opens `SaveImage` on the source and passes that reader straight to `LoadImage` on the
target, through a counting reader. Nothing is written to disk and no temporary tar exists.

*Why:* the feature exists to avoid a registry; a staging file reintroduces a full copy on disk,
a cleanup problem, and an at-rest copy of potentially sensitive image contents. Streaming keeps
memory constant (a fixed buffer) and makes the one request on each side the whole operation.

*Alternatives rejected:* a temporary tar (needed only for retry or offline inspection, neither
of which is in scope); buffering the archive in memory (unbounded for real images).

*Consequence:* a failure or cancel mid-stream cannot resume; the operator repeats the action.
Content-addressed layers the target already accepted are harmless and may remain, which the
job says.

### D2 — One platform: the one the image is inspected as

The platform is resolved before the dialog (source `ImageFacts`), named in it, sent on save and,
where the target's API supports it (≥1.48, the same guard `SaveImage` uses), on load. Older
targets get no `platform` parameter and load the one variant the archive carries.

*Why:* it matches Images › `w` save archive and the desk's existing image identity model, and
one save/one load is the smallest correct request pair. A multi-platform transfer would need a
manifest-list save the desk does not perform and target-store behavior it cannot assume.

*Alternatives rejected:* looping every platform (several streams, partial results, a much
larger confirmation); letting the target choose silently (the operator could not tell what was
copied).

### D3 — Keep the source tag; do not retag

A tagged row is saved by its reference, so the tag travels in the archive and the load creates
it (or moves it) on the target. An untagged row is saved by image ID and arrives untagged.

*Why:* load already carries the tag; retagging is Images' `t` with its own confirmation,
overwrite warning and pin, and should not be duplicated inside a copy. Per the operator's
choice, the copy names no destination tag.

*Alternatives rejected:* an editable destination reference, which would require loading by ID
(so the source tag is not created on the target) and then `TagImage` — two target mutations with
a leftover untagged image if the tag fails; loading the tagged archive and then removing the
unwanted tag — a deletion the operator did not ask for.

### D4 — The destination is a connected session member, chosen with the picker

`C` opens `dlgPicker` with every member except the focused source. Connected members are
choosable; disconnected ones are listed dimmed with the reason and cannot be chosen. No second
daemon → a refusal naming `M`. The chosen member is identified by its daemon key in the plan,
not by its position.

*Why:* only members have a live engine; adding a context can require a terminal answer (forbidden
while the desk is open) and is `M`'s job. A key survives focus changes and reconnections.

*Alternatives rejected:* adding a context as part of the flow (a second connection path with its
own errors); typing an endpoint (needs new endpoint validation and connection UI).

The generic picker gains `disabled`/`reason` on `pickItem`, rendered dim and ignored by Enter;
this is a small addition other pickers can use.

### D5 — Two reads before the dialog: source facts and target facts

`C` captures the row by image ID and reference, then one background command (10 s timeout, the
`prepare` pattern) does: `plan.Take` on the source, `ImageFacts` on the source for the platform
and pin, and `ImageFacts` on the target for the reference the copy will carry. The plan is built
from those answers; nothing that changes a daemon is sent.

*Why:* the confirmation must name the platform, the image's short ID, and whether the target tag
is new, names this image (blocked as already present) or names another image (moved by the load).
Discovering that only at job time would make the dialog a guess.

*Alternatives rejected:* one inspect at job time only (the dialog could not state the target
effect); reusing the Details pane's cached inspect (only valid for the highlighted image, and it
does not read the target at all).

### D6 — A copy plan is marked, and has its own executor

`Plan.Copy *CopySpec` marks the plan; its one step is `OpCopyImage`. `plan.Execute` refuses a
copy plan, and `plan.ExecuteCopy(ctx, src, dst engine.Engine, p, run)` executes it. The UI's
`runPlan` dispatches on `p.Copy != nil` and starts the job with both engines looked up from the
session at that moment.

*Why:* `Execute` is defined by taking one engine; widening it would touch every caller and test
and blur "one daemon per plan". Engines must not be stored in a `Plan`: it is rendered data, it
would go stale across reconnections, and it would make plan construction impure. Push already
establishes a plan kind with its own execution branch.

*Alternatives rejected:* changing `Execute`'s signature for all plans; putting engines or
`session.Member` values inside the plan; a separate non-plan package that would duplicate the
pinning and rendering rules.

### D7 — `Engine.LoadImage` decodes the load stream itself

```go
LoadImage(ctx context.Context, content io.Reader, platform Platform,
    progress func(LoadProgress)) (LoadResult, error)

type LoadProgress struct {
    Status          string // "Loading layer", …
    ID              string // layer id, when the daemon names one
    Current, Total  int64  // bytes, when reported
    Text            string // any plain stream line
}
type LoadResult struct {
    Refs []string // "Loaded image: ref" lines
    IDs  []string // "Loaded image ID: sha256:…" lines
}
```

The engine reads JSON messages from the response (the moby client returns only a read-closer),
maps `error`/`errorDetail` to `*engine.Error`, and keeps the daemon's own final lines. The
platform is sent only on API ≥1.48. `readOnlyEngine.LoadImage` returns `ErrReadOnly`, and
`TestReadOnlyCoversEveryMutation` forces that override to exist.

*Alternatives rejected:* letting the UI parse the stream (engine boundary leak); a quiet load
(no progress to show); re-reading the archive to verify (the stream is one-shot).

### D8 — The job is recorded under the target, with the source in `Job.From`

`jobs.Manager.StartOnPair(target, source, title, target, fn)` creates one job with `Daemon` =
the target (the write) and `From` = the source (the read). `CloseDaemon` and `RunningOn` match
either field, so `Session.Remove` refuses removal of either while it runs. The Jobs pane shows
`on <target> from <source>` when `From` is set; the status line uses the same phrase.

*Why:* the write is what the operator is watching land, but removing the source kills the read
just as surely. One job is the honest record; two ledger entries would double-report progress and
outcomes.

*Alternatives rejected:* recording under the source (the target could be removed mid-load); a
new job kind with a list of daemons (a larger API for one caller); refusing source removal only
in the UI (races with the session's removal path).

### D9 — Verify content, not the image ID

Before streaming, the executor re-reads the source `ImageFacts` and refuses if the reference no
longer names the pinned ID. After a clean load it reads the target `ImageFacts` for the reference
(or the ID for an untagged copy) and compares the ordered `DiffIDs` and the platform. Success is
reported with both image IDs; a mismatch fails the job and names the first differing fact.

*Why:* a classic image store names an image by its config digest; a containerd store names it by
an index/manifest digest. The same build loaded across those stores has different IDs, and
requiring equality would fail the exact cross-engine promotion this change is for. Diff IDs come
from the config's `rootfs.diff_ids` and are store-independent; the platform catches a variant
mix-up. `ImageFacts` is one inspect per side and the desk already treats it as the comparison
form of an image.

*Alternatives rejected:* strict ID equality (false failures across stores); trusting the load
stream alone (a tag could have been moved by another process before we looked); comparing only
size/platform (too weak to catch a different build).

*Residual race:* Docker has no conditional load. The target reference is checked at dialog time
and again immediately before streaming (refusing if its owner changed since the dialog), but a
move between that check and the daemon's own load cannot be prevented; the dialog already says
the tag may be created or moved.

### D10 — Safety follows the existing gates

The action is `mutating: true` and `needsRow: true` without `setCapable`, so `invokeOn` refuses
read-only sessions and selections before anything opens. `--no-registry` has no effect; no
registry endpoint is touched. The target engine obtained at job time is the session's own
(read-only wrapped when the session is), so even a UI bug cannot write to a read-only target.
A target whose link is insecure adds a warning that the image's contents cross that connection
unencrypted, mirroring the push warning for credentials.

*Alternatives rejected:* a new permission flag (nothing here needs one); blocking remote targets
(the feature is most useful across engines, often remote).

### D11 — Target list and notices

The target is not focused, so no list is refreshed for it during the copy. Its event stream sees
the image event and marks its lists dirty; focusing it later refreshes them as for any background
member. The job's success/failure is announced in the status line with both daemon names; no new
event type is added.

*Alternatives rejected:* switching focus to the target automatically (surprising and would drop
the operator's place); requesting a target refresh from the job (the background member's hub is
the owner of that state).

## Risks / Trade-offs

- **A real daemon may parse the stream differently from the fake.** The fake implements
  `POST /images/load` by reading the tar's `manifest.json` and config; engine tests cover the
  JSON decoding with a stub server. No mutating real-daemon test is added, consistent with the
  test guard. Mitigation: parse permissively (unknown messages become job log text, not errors)
  and show the daemon's own lines.
- **Large images and slow targets.** Memory stays bounded, but a stalled target can hold the
  job open indefinitely. Mitigation: the job is cancellable; the engine's HTTP client and the
  source stream close on cancellation. No new timeout is invented.
- **Cancel/failure leaves layers.** Content-addressed layers may remain on the target with no
  image or moved tag. Mitigation: the job says exactly that, and the source remains unchanged.
- **Removal refusal is broader.** A copy blocks removal of two daemons, not one; the refusal
  names the copy job so the operator knows why. This is a deliberate consequence of D8.
- **Verification inspects twice.** Two extra `ImageFacts` requests per copy are small next to
  the archive; they give the pin and the content check. Mitigation: both happen inside the job,
  never on a list-refresh path.
- **The target's tag effect can still race.** See D9's residual race; the confirmation already
  states that a target tag may be moved, and the target re-check narrows the window.
- **A disconnected target after load.** Verification then fails even though the load may have
  succeeded; the failure message says the target could not be read and that the load may have
  completed. The operator can inspect the target after it reconnects.

## Migration Plan

No stored state, schema, flag or configuration changes. The action appears only when a session
has two connected daemons; nothing changes for a single-daemon session. Rollback is reverting
the code: no daemon-side data is created beyond the image the operator asked to copy.

Documentation to update with the implementation: README's Images key row and action table, the
"Several daemons in one session" section and the "What it does not do" paragraph (images leave
the cross-daemon limitation; volumes stay), a copy section beside Save and Push, and
`docs/DECISIONS.md`/`docs/TESTING.md` for the round's decisions and commands.
