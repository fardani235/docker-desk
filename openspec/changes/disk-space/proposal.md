## Why

The desk can prune with an exact preview, but it cannot say where the bytes are, which of
them are shared, or what removing one object would give back. The README records that as a
gap ("does not show disk-usage accounting"). The numbers the operator needs are mostly the
daemon's own — `GET /system/df` states them — and the rest follow from figures the daemon
reports per layer. Everything is a GET.

## What Changes

- **A Space view.** `S` from any pane opens a full-screen view in the style of the help
  screen; `S` or Esc closes it and the desk is exactly as it was (focus, kind, highlight).
- **Totals as the daemon states them** — images, containers, local volumes, build cache,
  each with its reclaimable part — from one `GET /system/df`, run as a cancellable job when
  the view opens (and on `r`). Never recomputed; formatted as `docker system df` prints them.
  On a daemon older than API 1.52, which does not state totals, the client library's sums
  are shown and labelled as such.
- **Per image: size, unique size, shared size** (the daemon's `Size` and `SharedSize`). No
  total is ever presented as freeable. **Shared bytes are listed once**, against the exact
  set of images that hold them, from the daemon's per-layer sizes (image history) matched to
  the layers' chain IDs (image inspect).
- **Two numbers for the highlighted image:** what removing it alone frees (the daemon's
  unique size), and what removing it together with every image sharing its layers frees.
- **Writable layers per container** from `GET /containers/json?all=1&size=1`, as a separate
  cancellable job started with `m` — never part of a list refresh.
- **Archive attribution** for images already analysed (Images › Findings): size by layer and
  the largest build instruction, from the cached report. No archive is read for it.
- **Provenance and age on every figure** (daemon total, computed writable layer, computed
  from layer sizes, archive); anything taken before the last refresh that changed the
  engine's lists, or before the view's own last re-read, is labelled stale.
- **Enter jumps** to the highlighted object in its own kind.
- **Reclaim only through the existing dialogs:** `d` opens the single-object removal, `X`
  the prune preview. Their confirmations now state the bytes that would actually be freed.
  No new delete path, no prune endpoint, no force.
- **Read-only mode:** every figure, no reclaim entry point.
- **The fake daemon** gains layers shared by chain ID, image history, `shared-size`,
  `size=1`, `/system/df` (current and legacy shapes), volume sizes and build-cache records,
  cross-checked with the real `docker system df`.

## Impact

- `internal/engine`: read methods `DiskUsage`, `ImageSizes`, `ImageLayers`,
  `ContainerSizes`, and their types. The read-only wrapper passes them through.
- New `internal/space`: chain IDs, history mapping, sharing groups, what a set frees. Pure.
- `internal/ui`: the Space view, its jobs, keys, hints and help; freed-bytes lines in the
  remove and prune confirmations.
- `internal/fakeengine`: the endpoints above; demo images share base layers.
- `scripts/cli-crosscheck.sh`: `docker system df` against the fake.
- No new module.
