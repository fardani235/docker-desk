# Design

## Context

See `proposal.md` — Why. Five things in the current code and the current Engine API shape the
approach.

**The tab machinery is small and consistent.** `kindTabs` (`internal/ui/details.go:30`) lists a
kind's tabs; `renderTab` (`details.go:908`) dispatches; `[` and `]` cycle (`details.go:43`); a tab
that is a cursor list handles its own keys before the shared ones (`keys.go:344`), as Findings
does; `W` is routed per tab in `capture.go:91`; the footer and the help come from the same
tables. A tab is a state struct on the model, a sync hook called per frame (`model.go:753`), a
render function and a key handler.

**The analysis already reads everything this tab needs, once.** `startAnalysis`
(`internal/ui/findings.go:227`) streams the archive as a job, `forensics.Analyse` walks it and
produces a `Report` with per-layer digest, sizes, counts, whiteouts, instruction and findings,
and `findingsState.cache` keeps it by image ID and platform (`findings.go:36`). The Findings tab
starts it when shown, with a consent gate on a remote image over the threshold and `c` to cancel.
The Layers tab needs a report-shaped addition (below), not a second read.

**The archive's config history is the only exact step list.** The OCI image config carries
`history[].empty_layer`. `internal/forensics/rules.go:137` already reads it, binds the entries
without it to the layers in order, and notes when the counts disagree. It keeps only
`Instructions[]`, one per layer, so the steps that made no layer are dropped.

**The daemon's history is not that list.** `GET /images/{id}/history` reports `EmptyLayer` and,
on this machine's Docker 29.10 engine, reports it false on every entry: 18 entries, `false` on
each, for an image with 7 layers, and the config marks 11 of them empty. The desk already reverses
the daemon's newest-first order (`internal/engine/moby.go:640`) and already maps history sizes
onto layers only when the count cannot be wrong (`internal/space/space.go:45`).

**Sharing data is per image in the API, and per chain in the desk's only index.** The daemon
states `Size` and `SharedSize` per image and per-layer sizes in the history, but no holder set.
Space derives holders by chain ID (`internal/space/space.go:83`), which is right for the bytes a
classic store keeps once and misses content repeated at another depth. Nothing in the per-image
tabs uses it. Only diff IDs (`RootFS.Layers` in inspect) can be compared across images, and
inspect is one request per image.

Measured on this engine (11 images, the containerd store):

| image | layers | history entries | empty entries | what it shares |
| --- | --- | --- | --- | --- |
| `nginx:latest` | 7 | 18 | 11 | L1 content with `redis:latest`; `SharedSize` 117.5MB |
| `redis:latest` | 7 | 13 | 6 | L1 with `nginx`; the empty layer with `infisical-backend` |
| `infisical-backend` | 26 | — | — | 34 bytes: the empty layer, at five depths in its own chain |

## Goals / Non-Goals

**Goals:**

- One list a person can read top to bottom: the build, its cost, and what is shared.
- Every figure traceable to one of three sources: the daemon's history, the archive, or the
  engine-wide layer read.
- One archive read per image per session, shared with Findings; one layer read per image per
  session, shared with Space.
- The tab answers its main question before the archive is read, and answers it exactly after.

**Non-Goals:**

- A new report document, a JSON export, or a change to the exported image report.
- A per-step file list, a jump from a step into the Files view, or a filter of the Files tree by
  layer.
- A second byte accounting. `disk-space` owns bytes; this tab names content holders.
- An order key. The list is build order; the summary and the column carry the sharing.

## Decisions

### D1 — A step is the row, not a layer

One row per entry in the image's history, including the entries that made no layer. The layer a
row made is a column, not the row's identity.

| Considered | Why not |
| --- | --- |
| One row per layer | Loses `ENV`, `CMD`, `LABEL` and `ARG`, which are most of a modern image's history (11 of 18 for `nginx`), and cannot answer "what did the build do" for them |
| Two lists, layers and config steps | Two cursors, two orders, one build split in half |

The cost: the rows are re-bound when the exact list arrives (D3), and a config-only row has
no size, no files and no holders to show.

### D2 — A cursor list with a block under it, not a text view

Findings' shape: rows at the top, the highlighted row's facts below, the list scrolling when the
rows exceed the room. `tabView()` (`internal/ui/search.go:179`) stays untouched, so no `/` find.

| Considered | Why not |
| --- | --- |
| A text view like Changes | Gets find and `W` for free, but each step takes four or five lines, so 26 layers become 130 lines and the sharing column disappears; the operator cannot scan for what is shared |
| A section per step inside Findings | Findings is a list of rule results, not a build; mixing the two orders loses both |

The cost: more state (cursor, offset, list height) and a `W` builder that differs from the drawn
rows (D9).

### D3 — The archive's config history is the authority; the daemon's is a labelled fallback

The exact list comes from `Report.Steps` when the analysis has finished and the config's history
is present. Before that, the list comes from the daemon's history plus the existing size mapping:
an entry with a non-zero size is bound to the next layer only when the count of such entries
equals the layer count; otherwise every row says the binding is unknown, with the reason. The
daemon's `EmptyLayer` is never consulted.

| Considered | Why not |
| --- | --- |
| Trust the daemon's `EmptyLayer` | False on every entry of a 7-layer image on this engine; it would claim 18 layers |
| Wait for the archive before drawing any row | The tab is blank on a large remote image until consent, and on an image whose archive cannot be read it is blank forever |
| Read the config blob alone with a second, small request | There is no such endpoint; `GET /images/get` is the only source, and it is the whole archive |

The cost: the row set is attributed by counting until the archive arrives, and the tab must say
which of the two sources is in use.

### D4 — Sharing is by layer content, not by chain

A layer is shared when another image holds the same diff ID, at any depth. The image itself is
excluded, and a holder that contains the same diff ID several times counts once.

Two facts force this. The empty layer's diff ID appears at five depths inside
`infisical-backend`, so chain IDs split it into five unrelated layers; its blob is nevertheless
the whole of that image's `SharedSize` (34 bytes). And two images can hold the same content at
different depths, which the classic store keeps as two copies but the containerd store keeps as
one blob.

| Considered | Why not |
| --- | --- |
| Chain IDs, as Space uses | Misses the empty layer entirely, and any content repeated at another depth: the chain-derived holder sets find nothing shared for the image whose 34 shared bytes are exactly that blob |
| Both identities in two columns | Two answers to one word; the pane has room for one |
| Detect the store and pick the identity | `DriverStatus` says `io.containerd.snapshotter.v1` here, but the API does not promise a store name on every platform, and the question ("do I build on the same content") has one answer |

The cost, recorded in the README and in the decision record: Space counts bytes and this tab
counts content, so the two can state different sets. Space keeps its chain model; nothing in
`disk-space` changes.

### D5 — The engine-wide read is a key, one job, one cache

The tab reads the highlighted image's own layers as soon as it is shown — one inspect, which the
Details pane has already made, and one history, for the cheap step list (D3). `s` starts what
costs more: a job that reads the layers of every listed image it does not already hold. The tab
states the count before it starts (`reading the layers of 412 images, one inspect each`) and the
Jobs pane shows the progress and cancels it. The cache is per image ID and shared with Space,
which reads the same data; a second view, or a second image, reads only the new images.

| Considered | Why not |
| --- | --- |
| Start it on open, as Space does | Space is the accounting view; this is one image's tab, and on a 412-image remote engine the operator did not ask for 412 requests |
| A count threshold above which the key is required | A threshold that exists to hide a cost is worse than a line that states the cost |

The cost: the sharing figures need one keypress the first time in a session. The line that asks
for it says exactly what it buys and what it sends.

### D6 — The report keeps every step

`forensics.Report` gains `Steps []Step`, each with the masked instruction, whether the config
marks it `empty_layer`, and the layer it made (0 when none or when nothing binds). It is filled
where `layerOfHistory` is built (`internal/forensics/rules.go:137`), from the same array the
instructions come from, so there is one parse and one masking rule. `internal/report/image.go`
builds its own document and does not map the field, so the exported report is unchanged.

| Considered | Why not |
| --- | --- |
| Rebuild the step list in the UI from the daemon's history | It cannot be exact: `EmptyLayer` is false for everything here |
| Keep the raw config in the report and mask in the UI | Copies secret-shaped text into a second place with a second rule; the report's own rule is the one tests cover |

The cost: the report grows by one slice (tens of entries); a reader of `Report` must know that
`Instructions` and `Steps` describe the same array.

### D7 — Two byte figures, each named

The row shows the daemon's history size for the step, which is the number `docker history` shows
and the one available before the archive is read. The block adds the archive's two figures: the
bytes the layer's files hold, and the bytes the archive stores for it. The summary line names the
source of the sizes in use.

| Considered | Why not |
| --- | --- |
| The archive's stored bytes in the row | The column's meaning would change when the analysis arrives |
| One figure only | `nginx` states 243.4MB while its history sizes add to 176.6MB, and the README records that this store counts a layer's compressed content as well as its unpacked files. A single number would hide the difference rather than show it |

The cost: three numbers exist and the tab must not add them as if they were the same unit. It
states the figures without explaining the daemon's accounting; the README carries that.

### D8 — Appended after Upstream

`Info, Inspect, Findings, Upstream, Layers`. No existing tab moves, so no operator's `[`/`]`
count changes.

| Considered | Why not |
| --- | --- |
| After Info, before Inspect | Reads better (the cheap structural view first), but moves three tabs and changes every existing position |

The cost: at 120 columns the strip is long, and at 80 it collapses to `[Layers] 5/5`.

### D9 — `W` writes the build, and no new report class

`W` renders the whole list plus every step's block at the page width through the existing capture
machinery, and writes it as a text capture under the text-capture rules. No new document, no new
schema.

| Considered | Why not |
| --- | --- |
| Extend the JSON image report with steps and holders | A schema change, a version question and a second consumer, for data one text file already carries |
| No capture at all, as Findings has no pane capture | Findings has `w`, its own report. This tab would then be the only computed view that cannot be kept |

The cost: a builder that renders all rows, not only the visible ones, and a test at two widths.

### D10 — One layer cache, owned by the desk

`spaceState.layers` moves to a desk-level cache of `ImageLayers` by image ID, used by both the
Space job (`internal/ui/space.go:146`) and the Layers tab. Each read is still one inspect and one
history call per image, made once per session.

| Considered | Why not |
| --- | --- |
| A second cache in the tab | Two views on one session would double the requests to the same engine |
| The tab reads only images it already knows | A partial holder set presented as the answer is worse than none |

The cost: a refactor of the Space view's job and its tests, which this change must not otherwise
disturb.

### D11 — What the tab refuses to say

- It never says a layer's bytes are stored once; that is Space's claim, and it depends on the
  store.
- It never says a holder is the only holder when some images were not read; the summary says
  `at least` and the number not read.
- It never attributes a step to a layer from a count it cannot trust, and it never shows a raw
  `created_by` string: the masked form is the only form.
- It never calls a config-only step "empty" in the Docker sense without saying what it did: the
  block says the step changed the configuration, not the files, which is what `empty_layer`
  means.

## Risks / Trade-offs

- **The two step lists can differ in length.** The daemon's history and the config's history are
  the same array in practice, but a daemon that omits or merges an entry would shift the rows
  under the cursor. → The cursor is an index, clamped; the block follows the index, and the header
  says which list is shown. A test covers a report with fewer steps than the daemon's history.
- **The binding flips under the operator.** Before the archive, rows are attributed by counting;
  after it, exactly. → The row's key is its index, the cursor stays on the same index, and the
  header line changes from "counted" to "the archive's history".
- **A 412-image read is slow.** → One job, cancellable, progress in the Jobs pane, cached per
  image; the tab says the count before it starts.
- **`Report.Steps` duplicates `Instructions`.** → It is filled in one place from one parse, and a
  unit test asserts that `Steps[i].Layer` and `Instructions[i]` agree for every layer.
- **Memory for the content index.** One map entry per (diff ID, image) pair: about 16,000 entries
  for 400 images of 40 layers. → No bound needed, but the same read is bounded by the image list
  the desk already holds.
- **The sharing column can be read as bytes.** → The column is a count of images, the block names
  them, and the summary says "layers", never "bytes shared", except for the own-bytes figure,
  which names its source.

## Migration Plan

None. A new tab, no stored data, no change to an existing request or response, and every request
it makes is a `GET` the desk already makes elsewhere.

## Open Questions

None that change the specs or the tasks. Whether sharing deserves an order key (`o`, as Findings
has) is deferred: the summary line and the count column answer the question in build order, and an
order can be added without changing any requirement here.
