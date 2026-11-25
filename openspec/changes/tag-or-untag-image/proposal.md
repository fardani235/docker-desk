# Proposal

## Why

The desk can pull an image, remove it and prune unused ones, but it cannot give an image a
local tag or take one away. Moving a build between names — the ordinary step before pushing or
running it under another repository — still means leaving the desk for `docker tag`, and the
only in-desk way to remove a reference is `d remove`, which deletes the image when it was the
last tag. A registry round and a local workflow both need the two small, local operations.

## What Changes

- Images gains `t` (tag): a form naming the highlighted image (by ID), its current references
  and the new `repo[:tag]`. A typed reference without a tag means `:latest`. The form says the
  tag is created locally, no registry is contacted, and if the target already names another
  image it names that image and warns that the tag will move.
- The tag is created as a background job and the image list refreshes. The source is always the
  exact image that was highlighted; if the target moved between the form and the request, the
  job refuses rather than moving a tag the operator did not see.
- Images gains `u` (untag): a typed confirmation (type the tag) that removes one reference and
  keeps the image data, only when at least one other reference remains. On an image's last tag
  it refuses and points to `d remove`, because Docker cannot remove the last reference without
  deleting the image.
- Both actions are one image at a time, refused in read-only mode, and allowed under
  `--no-registry` because they touch only the local image store. Neither forces, retags more
  than the one reference asked for, or contacts a registry.
- The engine gains one local mutation, `TagImage`, with the read-only refusal and a fake-daemon
  endpoint; untagging reuses the existing image-removal request and its pin/`KeepsData`
  guards.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `image-operations`: adds tagging (form, normalization, source pin, move warning) and
  untagging (typed tag, last-tag refusal, data kept), alongside the existing pull, remove and
  prune actions.

## Impact

- `internal/engine`: `TagImage` (moby `ImageTag`), the read-only refusal, and
  `internal/fakeengine`'s `POST /images/{name}/tag` with its request log and collision behavior.
- `internal/plan`: target normalization, a tag-collision lookup, and `ImageUntag` (the existing
  `KeepsData` removal planning, refusing the last reference).
- `internal/ui`: the `t` form, the `u` action and typed dialog, job/refresh handling, hints and
  help.
- `README.md` (Images keys, tagging/untagging section, "What it does not do"), `docs/DECISIONS.md`,
  `docs/TESTING.md` and the Images screen captures.
- Tests: engine request/read-only/fake tag, normalization and collision, untag last-reference
  refusal and pin, tag form validation and move warning, a target moving between form and job,
  read-only, and an end-to-end pty tag/untag.
- No new dependency; the moby client already exposes `ImageTag`.
