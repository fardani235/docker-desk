# Proposal

## Why

The desk can say what an image declares, which packages it installs, how one image differs
from the one a tag names, and whether a tag has moved — but not what is inside it. "Where did
400 MB of this image go" and "which build step put this file here" have no answer short of
`docker run` and `du`.

The archive the desk already streams for Findings holds every path the image has, with its
size, mode and the layer that wrote it; the same history maps each layer to the instruction
that produced it. An index of that walk is a second projection of a read the program already
performs, and a container's own writes are one cheap Engine API call away.

## What Changes

- A **Files** view: a full-screen overlay in the family of Packages, Space and Repository,
  opened with `F` on the highlighted image or container. It opens on facts only — identity,
  layer count, the daemon's size for the image — and states that nothing has been read;
  `Enter` runs one cancellable job that reads the archive and builds the index.
- **An image's tree**: its layers merged, each path with type, size, mode, the layer that
  introduced it and that layer's build instruction. Whiteouts remove a path, an opaque
  directory hides its lower contents, hard links are one entry pointing at another, symlinks
  are shown and never followed, sockets, FIFOs and device nodes are typed and never opened.
- **A container's files** are the image's tree with `GET /containers/{id}/changes` laid over
  it: paths added, changed or deleted since the image, fetched when the view opens and cheap.
  Inherited rows keep the image's size and attribution; an added path has no size and cannot
  be expanded; a deleted path is shown struck through; a changed file's size is labelled as
  the image's.
- **The archive is the round's only expensive read and happens only when asked.** Opening the
  view on a container whose image has not been indexed shows its changes and says the image's
  files have not been read.
- **Nothing about file contents.** No file's bytes are read, shown or searched; the index
  keeps names, types, sizes, modes and layer numbers. Names are attacker-controlled, so the
  screen and the export escape control characters, newlines and invalid UTF-8.
- **Bounded**: at most 200,000 entries and 4 KiB per path; the view says how many were kept
  of how many were seen, and the filter says it may miss.
- **Export** (`w`): the listing as text or JSON, through the existing evidence-file dialog; no
  new way to write anything.
- Out of scope: reading a container's own archive (its own later change), any file-level
  difference between a container and its image, file previews, and anything mutating.

## Capabilities

### New Capabilities

- `files-explorer`: the Files view — its two sources, the archive index, layer attribution,
  the container overlay, the bounds and refusals, and the export.

### Modified Capabilities

None.

## Impact

Code: a new `internal/files` (the index, override, directory rollups, ordering, filter,
flattening, all pure), `internal/forensics` (a headers-only mode and a files projection of the
walk it already makes), `internal/engine` (+`ContainerChanges`, a `GET`), `internal/fakeengine`
(a seeded change list per container), `internal/ui/files.go` (the view, its keys, the export),
`cmd/fake-dockerd` (sandbox containers that wrote something), `internal/e2e` (screens),
`README.md` and `docs/DECISIONS.md`.
