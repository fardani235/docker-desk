# Proposal

## Why

The desk notices a moved tag — `supply.tag-moved` says a container runs bytes its tag no longer
names, and that which of the two images is intended is unknown — but never says what differs.
An operator deciding whether to re-create the container has to find out elsewhere.

## What Changes

- A sixth Details tab on Containers, **Changes**, after Info.
- Drawn at once from four GETs at most (inspect and history of each image): both identities,
  the layer lineage from the ordered diff IDs with the instructions of the layers outside the
  shared part, and the config difference — environment variables by name only.
- `Enter` compares the contents as a cancellable job, reusing Findings' analysis and cache:
  packages added, removed and version changed; archive findings that appeared or disappeared.
- Every case with nothing to compare says why; every state says that restarting keeps the
  image and only re-creating takes the new one, which the desk does not do.

## Capabilities

### New Capabilities

- `image-changes`: the pair, the cheap comparison, the content comparison, the refusals.

### Modified Capabilities

- None. Findings' rules are unchanged; `forensics` exports the reference resolution and the
  instruction cleaning they already use.

## Impact

- `internal/engine`: `ImageFacts` (read-only; names, not values, of environment variables).
- `internal/changes`: new, pure.
- `internal/ui`: `changes.go`; the tab, Enter and c on it, hints and help.
- `internal/fakeengine`: per-image config, `SetImageFixture`, `SeedMovedTag`, `SeedChanges`;
  the demo gains `report-job` on an older build of `acme/report:1.4`.
- No mutation, no write to disk; read-only mode unchanged.
