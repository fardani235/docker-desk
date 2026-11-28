# Proposal

## Why

The desk can say what an image contains (Findings, Files, Packages), what it costs (Space), and
how it differs from another build (Changes, Upstream). It cannot say what the build did, step by
step, or which other image holds the layer a step made.

Two facts about the current API and the current views make that gap real. The daemon's history
cannot say which steps made a layer: on this machine's Docker 29 engine,
`GET /images/nginx:latest/history` returns 18 entries and reports `EmptyLayer: false` on every
one, while the image has 7 layers. And no per-image view says which other images hold the layer a
step made: the daemon states one `SharedSize` per image with no holders, and Space derives holders
across the engine by chain ID, which misses content repeated at a different depth. So a person
looking at a 5.26GB image can see that it shares 34 bytes and looks at 26 layers with no idea
which steps produced them or what they wrote. Docker's own `history` shows the steps but not the
contents, the files, the findings or the holders.

## What Changes

- Images gains a `Layers` tab: one row per build step, in build order, oldest first, so `L1` is
  the base layer as in Findings and Files. Each row carries the step's instruction, its size, the
  files it laid down, and, once the layer read is made, how many other images here hold the layer
  it made. A step that made no layer is a row too, marked as config only.
- The step list comes from the archive's config history when the image has been analysed, where
  `empty_layer` binds a step to its layer exactly. Before that it comes from the daemon's history,
  where a step is attributed to a layer only when the size count cannot be wrong, and the tab says
  `unknown` otherwise. The daemon's `EmptyLayer` field is never trusted: this engine reports it
  false for every entry.
- The highlighted step's block shows the full instruction (build-argument values left out and
  secret-shaped text masked, through the rule Findings and Changes already use), the layer's
  content digest and whether it matches the config's `diff_id`, the bytes its files hold, the
  bytes the archive stores for it, what it added, replaced or deleted, which images hold the same
  layer content, and the findings about that layer.
- A summary line states how many of the image's layers another image here holds, and what the
  image's own layers cost. Both figures name their source and say when they are not known.
- Sharing is by layer content, not by chain: the empty layer's diff ID sits at five different
  depths inside one image on this engine, and its blob is the whole of that image's shared bytes.
  The image itself is never counted, and the same content at two depths counts once per holder.
- The sharing read needs one inspect per image and is expensive on a large engine, so the tab
  reads only the highlighted image's own layers when it opens, and every other image only when `s`
  is pressed, after the tab says how many that is. It runs as one cancellable job and shares its
  per-image cache with the Space view, which already reads the same layers.
- The tab reuses the Findings analysis: one job, one report cache, the same wait for consent on a
  remote image over the size threshold, the same `c` to cancel and the same refusal wording for an
  image with no readable archive. It sends only `GET` requests and works in read-only mode.
- `W` writes the whole build sheet, every step, not only the rows in view, through the text-capture
  rules. The prefilled name gains the kind `layers`.
- The tab is appended after Upstream, so no existing tab's position changes. `[` and `]` reach it.

## Capabilities

### New Capabilities

- `image-layers`: the Images › Layers tab — one row per build step, the exact step-to-layer
  binding from the archive's history, the highlighted step's facts, sharing by layer content with
  the images that hold it, the cost of the engine-wide read and the key that starts it, the shared
  job and cache with Findings, and what the tab does not claim.

### Modified Capabilities

- `text-capture`: `W` is offered on Layers, and the prefilled name's kinds gain `layers`.

## Impact

- `internal/forensics`: the report keeps every history step — its masked instruction, whether the
  config marks it `empty_layer`, and the layer it made — beside the existing per-layer
  instructions, so the exact list survives the analysis. No new archive read, no buffer, no change
  to the exported report document.
- `internal/space`: a content identity beside the chain identity: the holders of a layer's diff
  ID, built from the same image list. The existing byte accounting is unchanged.
- `internal/ui`: the new tab (`layers.go`), its tab entry and render case in `details.go`, its key
  routing in `keys.go`, its capture in `capture.go`, its footer and help lines in `view.go`, and
  the shared per-image layer cache moved out of `spaceState` so the two views read each image
  once.
- Tests: the exact step list from a config with empty and non-empty entries, the fallback mapping
  and its refusal, sharing across images with the empty layer repeated at different depths,
  read-only, a remote image over the threshold, the capture at two widths, and the empty states.
- Documentation: the README's Images section and key tables, `docs/DECISIONS.md` (the next round),
  `docs/JOURNAL.md`, and the regenerated `docs/screens`.
