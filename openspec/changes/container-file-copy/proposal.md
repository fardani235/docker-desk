# Proposal

## Why

The Files view shows what is inside a container — its paths, types, sizes, modes and what the
container changed — but it cannot take a file out or put one in. The moment an operator is
looking at `/etc/nginx/nginx.conf` or at a database file the container just wrote, the next
action is a copy, and today that means leaving the desk for `docker cp`. The Files round
deliberately left reading a container's archive out (`docs/DECISIONS.md` V14, V22); this change
is that round, extended to writing.

## What Changes

- **Two keys in the Files view on a container.** `o` copies out from the highlighted path, `i`
  copies in to it. On an image the keys say copies are between a container and this machine; the
  image's listing export (`w`) is unchanged.
- **Copy out.** The container path comes from the cursor; the local path is typed. A file or a
  directory is copied with its contents (`docker cp -a` semantics), modes preserved, symlinks
  recreated and never followed. The daemon's archive is streamed and extracted as one
  cancellable job, so a changed or added file can be copied even though the view has no size
  for it. An existing local destination is refused; missing parent directories are refused; a
  partial extraction is removed on failure or cancel; entries that are not files, directories
  or symlinks are skipped and named in the job's output. It reads the daemon only, so it works
  in read-only mode.
- **Copy in.** The local path is typed and the container destination is prefilled from the
  cursor. The desk walks the source, then shows a confirmation naming the container, the local
  source (path, entry count and bytes), the container destination, and that files already at
  the destination are replaced. `y` starts the upload as one job; the source is streamed as a
  tar, never held in memory. Read-only mode refuses it before any form opens.
- **The destination is the path named.** Unlike `docker cp`, which nests a source inside an
  existing destination directory, a copy names the exact path it creates or replaces. The
  parent must exist. This makes the confirmation literal and makes "refuse an existing local
  path" meaningful; it is stated in the forms and recorded as a deliberate divergence.
- **The container's archive is read for a copy, not for the tree.** The index still never reads
  a file's contents, and the view's presentation is unchanged: an added directory still shows
  no sizes and cannot be expanded, and a changed file still shows the image's size.
- **Host safety rules.** No archive entry may escape the extraction directory, by an absolute
  name, `..`, or a symlink followed during extraction. Special files are skipped and listed.
  Nothing is overwritten on this machine without the operator naming a path that does not
  exist.

### Non-goals

- Copying between two containers, or between two daemons.
- A local file browser. The local side is a typed path (with `~` expanded), as the export
  dialog's paths are.
- Copying an image's files out, or changing the image's tree view.
- Showing sizes for paths the container added, previewing or hashing contents, or diffing a
  container against its image beyond the daemon's change list (V22 stays true for the tree).
- Preserving ownership, timestamps or extended attributes beyond modes. As with `docker cp`,
  ownership at the destination is the daemon's own default.
- A command-line copy flag, globs, or recursive filtering by pattern.
- Forcing an overwrite: an existing local path is never replaced, and no option is offered to
  remove it for the operator.

## Capabilities

### New Capabilities

- `container-file-copy`: copying a file or directory between this machine and a container from
  the Files view — the entry points, the two forms, exact-name destination semantics, recursive
  contents, modes and links, the tar stream in both directions, refusal of existing local
  paths and missing parents, skipped special files, partial-output cleanup, the jobs, and
  read-only behaviour.

### Modified Capabilities

- `files-explorer`: the view is no longer read-only on a container — it gains copy-in and
  copy-out entry points and their footer/help text — and the "no file contents" rule is scoped
  to the index, since a copy reads a container's archive on purpose.
- `mutation-safety`: copy-in is a mutation whose confirmation names the container, the local
  source and the destination, and states that files there are replaced; copy-out writes outside
  the daemon with its own consent and is allowed in read-only mode.

## Impact

- `internal/engine`: a read that streams a container's archive and a mutation that extracts one
  into a container. The read-only wrapper and its coverage test grow by the mutation; the
  methods take their own options types, as the engine layer does today.
- A new package for the transfer itself: building a tar from a local path and extracting one to
  a local path, with the name, traversal, symlink and special-file rules in one tested place.
- `internal/plan`: a copy-in operation and its step payload — the local source, the container
  destination, and what the plan promises — with a plan-time walk that counts entries and
  bytes for the confirmation.
- `internal/ui`: the Files view's two keys, their forms and the confirmation, the local path
  input with `~` expansion, the job wiring and cancellation, the footer and help text, and the
  read-only refusal.
- `internal/fakeengine`: a container filesystem and `GET`/`PUT /containers/{id}/archive`, so the
  engine, UI and end-to-end suites can copy without a real daemon; the real `docker cp` is added
  to `make crosscheck` to keep the fake honest.
- Tests: transfer safety (traversal, symlinks, devices, partial cleanup), engine request and
  response handling, plan and form behaviour, read-only, cancellation, and an end-to-end copy in
  both directions.
- Documentation: README (the two keys and the exact-name rule), `docs/DECISIONS.md` (a round
  seventeen), a captured screen of a copy, and `docs/TESTING.md`'s proof table.
