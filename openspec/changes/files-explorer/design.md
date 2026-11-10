# Design

## Context

See `proposal.md` for why. The pieces this design builds on already exist:

- `internal/forensics` opens `docker save` as one tar stream, walks each layer, and already
  understands both layouts (legacy and OCI), platform selection, attestation manifests,
  gzip layers, `.wh.` whiteouts, `.wh..wh..opq` opaque directories, symlinks and hard links.
  Its layer walk currently keeps per-path fingerprints only — 8 bytes each, capped at 2,000,000
  — because its questions (which package database copy counts, which layer added a file) never
  needed the name.
- `internal/forensics/fixture` describes layers as `Files`, `Delete`, `Opaque`, `Link`, `Type`
  and `CreatedBy`, so an image with a deep tree, a deletion, an opaque directory and a symlink
  is a fixture edit. `fakeengine` already serves it from `GET /images/get`.
- `internal/ui/packages.go` is a full-screen cursor view with virtualised rows, columns, a
  filter, a detail block and an export. The Files view is its sibling, not a new pattern.
- The vendored client has both `CopyFromContainer` (`GET /containers/{id}/archive`) and
  `ContainerDiff` (`GET /containers/{id}/changes`). Only the second is used in this change.
- `ImageDetail` carries `Layers` and `Platform()`, and the image list carries `Size`, so the
  view's opening facts need no request beyond the inspect the Details pane already loads.

Constraints that shape the approach: the desk never reads a file's contents; every figure names
its source and age; nothing expensive happens without the operator asking; the engine
interface is the only door to the daemon; the fake must speak whatever the desk speaks.

## Goals / Non-Goals

**Goals**

- One archive read per image per session, shared by every opening of its Files view.
- A pure index (`internal/files`) that answers merge, rollups, ordering, filtering and
  flattening, testable with no archive, daemon or UI.
- The image's tree and a container's overlay drawn by the same view code from the same model.
- Bounds and refusals that are stated, not implied.

**Non-Goals**

- Reading a container's own archive (its own change later).
- Any difference between a container's files and its image's beyond the daemon's own change
  list.
- Sharing the tree with the Findings report, previewing contents, or writing anything but the
  export.
- Any mutating request; the view is `GET`-only by construction.

## Decisions

### D1. A mode on the existing analyser, not a second archive parser

The archive-layout half of `forensics` is the expensive, subtle part (legacy vs OCI, nested
index, attestations, platform, per-layer ordering, whiteouts). A files package that parsed
archives itself would duplicate it and drift. Instead `forensics.Options` gains a `Mode`:
`ModeFindings` (the zero value, today's behaviour), `ModeFiles` (headers only — no rule
evaluation, no content reads, no secret scanning), and `ModeBoth`. In `ModeFiles` the layer walk
appends an entry per tar header and still records whiteouts and opaques.

Alternative rejected: always run `ModeFindings` and keep the tree. It would read up to 64 KiB of
every file and run every rule, which is most of the cost of a Findings analysis, for a view that
needs none of it.

### D2. The merge lives in `internal/files`, fed by per-layer entries

`forensics` reports what each layer holds — entries, whiteouts, opaques, layer order, history.
`internal/files` applies the overlay: later layers win, a whiteout removes, an opaque directory
clears what is below it, and each surviving path keeps the layer that last wrote it. The merge
also counts the paths a later layer removed, so the header can say how many disappeared without
drawing a row for any of them. This split keeps the archive reader about image structure and the
view model about a filesystem.

### D3. Attribution is the last writer; directories carry none

A file's layer is the last layer that wrote that exact path, and the row shows that layer's
history entry. A directory can be written by many layers and its size is computed, so a layer
number on it would be misleading: directory rows show no layer and no instruction.

### D4. Bounds: 200,000 entries, 4 KiB a path, 48 MiB of path text

Three limits, all counted, all reported:

| Limit | Value | Why |
| --- | --- | --- |
| entries | 200,000 | ~25 MB of index; a full Debian image is ~10k, a node_modules image a few 100k |
| one path | 4 KiB | Linux's own `PATH_MAX`; deeper is hostile, not useful |
| path text | 48 MiB | stops 200,000 × 4 KiB from being 800 MB of strings |

The walk keeps counting after a bound so the header can say "200,000 of 812,431", and the
filter states that a match may lie in the part that was not kept.

### D5. One index cache in the UI, keyed by the image's ID, at most two

The tree is 20–40 MB at the cap, so it is not carried in the Findings report: that report is
already cached per image, is serialised for `--report`, and must stay small. The Files view owns
its own cache (the current object and one previous), so reopening is instant and a given image's
archive is read once per session. A different feature reading the same archive again is visible
as a job, not a silent cost.

### D6. The container side is `ContainerDiff` plus a pure overlay

`engine.ContainerChanges(ctx, id)` returns `[]Change{Path, Kind}` from
`GET /containers/{id}/changes`. `files.Apply(index, changes)` marks each indexed path added,
changed or deleted and returns the counts of changes that fall outside the index. Paths are
cleaned the way the walk cleans layer names, so a change can never name something above the
root. The daemon's names are attacker-controlled too and take the same escaping as archive
names.

Deleted paths absent from the index are not drawn. They are counted and the header says so,
because the index may have been cut (D4) and silence would be a false absence claim.

Alternative rejected: reading the container's archive so its tree can be shown without its
image. It costs the image's bytes again for a few MB of difference, and it makes every container
file view a multi-gigabyte read; it is deferred to its own change with its own pre-flight.

### D7. Ages: the tree never goes stale, the changes do

An image's archive is immutable, so the index is drawn without a staleness marker once built.
A container's change list is read at open and on `r`; the header shows its age, and `r` sends
one small request and nothing else. The image's tree is not re-read on `r`.

### D8. Names are escaped once, by a pure function, for both screen and file

`files.Escape(string) string` maps control characters and `DEL` to visible `\xNN`, escapes a
newline as `\n`, and renders invalid UTF-8 as `\xNN` byte by byte. The screen draws the escaped
form and the export writes it, so a name cannot move the cursor, break a line, or forge a
listing row. CSV is not offered: quoting rules plus attacker-controlled content is a second
problem nobody asked for. JSON stores the escaped string, not `encoding/json`'s U+FFFD
replacement, so no byte of a name is lost or invented.

### D9. The view reuses the Packages idiom

`filesBox` follows `packagesBox`: a header, column titles, only the visible rows drawn, and a
detail block under the list. `Row` is produced by a pure `Flatten(index, expanded, filter,
order)`, so the frame cost is O(visible rows) and the tree's shape is testable without a
terminal. The name column truncates on the left (`…/local/bin/python3.12`) so the basename — the
part a person reads — stays visible.

### D10. One job, cancellable, and no partial index

`Enter` starts a job titled "read the image archive"; its status carries bytes, layers and
entries, and `c` cancels it. Cancelled, failed or broken, it keeps no index and the view says
why. The job's log and title carry no path and no byte of the image, the rule the analysis job
already follows. The cap (D4) is the only way an index is partial.

### D11. Entry point and keys

`F` opens the view from any pane when the highlighted kind is Images or Containers, and a status
sentence otherwise. Inside it, the overlay owns its keys: arrows and `j`/`k`/`g`/`G`, `Right` or
`Enter` to expand, `Left` to collapse or go to the parent, `s` to order by name or size, `/` to
filter, `w` to export, `r` to re-read a container's changes, `c` to cancel, `Esc` to close.
`Esc` restores focus, kind and highlight exactly, as Space and Packages do.

## Risks / Trade-offs

- **Memory at the cap.** 200,000 entries plus path text is tens of megabytes, and two caches
  double it. → The three limits in D4, a two-entry cache, and the index dropped when evicted;
  a test measures an index at the cap.
- **Two sizes that do not agree.** The daemon's image size and the tree's total measure
  different things. → Both named and labelled; the header says why, and a test asserts the
  sentence rather than a number.
- **A cut index hides a deletion.** A deleted path can fall outside the kept entries. → The
  count of changes outside the index is reported, and the filter says it may miss.
- **Hostile names on screen and in the file.** → One escape function used by both, with a
  fixture whose names contain ESC, a newline and invalid bytes; the test fails if a raw ESC
  reaches the drawn rows or the written file.
- **The archive read is the round's one expensive operation.** → It happens only on `Enter`,
  is cancellable, shows progress, and the view quotes the daemon's size for the image before
  the operator commits.
- **The change list is a snapshot.** A container can write after it was read. → Its age is
  shown, `r` refreshes only it, and the spec forbids claiming a path is absent from a stale
  list.
- **Directories a container created cannot be opened.** → The row says its contents are not
  read; the deferred archive round is the answer, not a silent empty directory.

## Migration Plan

None. The change is additive and read-only: no stored state, no schema, no daemon mutation, no
new dependency. Reverting means removing the view; nothing else observes it.
