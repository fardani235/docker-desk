## Context

The desk reads through `internal/engine` (reads, mutations, streams); read-only mode permits
GET and HEAD only, enforced twice. Every slow operation is a job; live data never arrives as
messages; nothing is guessed. The prune preview and the single-object removal are the only
ways anything is deleted, one request per object, never forced, and Docker's prune endpoints
are never called (D13–D15).

## Goals / Non-Goals

**Goals:** report the daemon's disk account verbatim; separate shared from unique bytes per
image without double counting; say what one removal — and the removal of an image together
with everything sharing its layers — frees; measure writable layers only when asked; label
every figure with where it came from and how old it is; route reclaim through the existing
dialogs; stay complete in read-only mode.

**Non-Goals:** a new way to delete anything; build-cache pruning (the desk has no builder);
predicting when a containerd garbage collection runs; walking any filesystem itself.

## Decisions

The full reasoning is in `docs/DECISIONS.md`, U1–U13. In short:

- **U1 Totals.** One `GET /system/df?type=…&verbose=1`, as the cancellable job "measure disk
  usage", when the view opens without a current account and on `r`. Totals are the
  daemon's, printed with the docker CLI's size format so they can be compared character for
  character. Below API 1.52 the daemon states no totals; the client library sums its lists
  as the CLI does, and the view says "summed by the client library (API < 1.52)".
- **U2 Unique and shared.** Per image the daemon's `Size` and `SharedSize`; unique is
  `Size − SharedSize`, the CLI's UNIQUE SIZE. A total is never labelled as freeable;
  reclaimable is "the daemon's estimate".
- **U3 Sharing by chain ID.** Layers are stored by chain ID, so two images share a layer
  when their diff-ID prefixes are identical. Diff IDs come from image inspect, per-layer
  sizes from image history (the daemon's own per-layer figure), matched by exact count as
  Findings does with instructions (F7). Cached by image ID — content-addressed, it cannot
  change. Each image's chain sums are checked against the daemon's `Size` and `SharedSize`;
  a mismatch is shown and the computed figures for that image are withheld.
- **U4 What a set frees.** The bytes of every chain whose holders all lie inside the set.
  Exact when every chain involved has a size and every present image is indexed; otherwise
  shown as "at least" the sum of the members' unique sizes, or unknown — never guessed.
- **U5 Writable layers.** Only `GET /containers/json?all=1&size=1`, only from `m`, as the
  cancellable job "measure writable layers". The df answer's per-container items are not
  shown: one source per figure.
- **U6 Archive attribution.** Read from the Findings cache only; size by layer and the
  largest instruction when the history maps. Labelled as file content from the archive, a
  different measure from the daemon's on-disk size.
- **U7 Provenance and age.** Every figure carries a source and the time it was taken. The
  desk records when a refresh last found the engine different — a list whose objects or
  container states differ, or an account whose totals differ — and anything taken before
  that is stale. A refresh that finds nothing changed ages nothing.
- **U8 The view.** A layer between the desk and its dialogs: keys go to an open dialog
  first, then to the view. The desk underneath is not touched, so closing restores focus,
  kind and highlight; they are also saved and put back explicitly.
- **U9 Enter.** Closes the view and highlights the object in its own kind, focused on the
  List; a filter that would hide it is cleared, and the status line says so.
- **U10 Reclaim.** `d` invokes the kind's existing remove action on the highlighted object;
  `X` the kind's existing prune. Every remove and prune confirmation gains a "frees" line,
  computed from the view's last account without a request: an image's unique size, nothing
  for an untag, the set figure (U4) for several images, measured writable layers for
  containers, account sizes for volumes — each with its source and age, STALE when the
  engine changed since, and "not measured" when there is no account. (Measuring at the
  moment of the confirmation was tried and dropped: it would walk filesystems outside U5's
  job.)
- **U11 Read-only.** All figures; `d`, `X` and their hints absent.
- **U12 The fake.** Layers with chain sharing; history with an empty-layer entry; `/system/df`
  in both shapes; `size=1`; volume sizes; build-cache records; `DiskUsageDelay` and
  `SizeDelay` to make the slow paths observable.
- **U13 Proof.** `docker system df` against the fake equals the view's totals.

## Risks / Trade-offs

- `/system/df` itself makes the daemon walk container and volume filesystems; that is why it
  is a job, never automatic outside the view, and cancellable.
- With the containerd image store, bytes are released by its garbage collector after the
  removal returns; the view says "frees" for what becomes unreferenced, not "free now".
- In the classic store a removal can also delete untagged parent images; their layers are in
  the child's shared size and are not claimed as freed.
