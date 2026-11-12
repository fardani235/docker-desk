# Design

## Context

See `proposal.md` — Why. The constraints that shape this work:

- The Files view already has the container identity, the cursor path and a full-screen key
  budget (`internal/ui/files.go`). Its tree is the merged image, overlaid with the daemon's
  change list; the index never reads contents (V14, V22).
- Forms are now a list of fields (`internal/ui/form.go`, D26): a text field, repeatable rows,
  a choice. Copy needs one editable path and, for copy-in, two.
- Export already resolves local paths through `report.ExpandPath` and refuses to replace an
  existing file (`internal/report/file.go`), and the export dialog is the model for "name a
  local path, then confirm".
- The engine wraps `github.com/moby/moby/client`; every mutation is overridden in
  `readonly.go` and counted by `TestReadOnlyCoversEveryMutation` (19 today). The fake daemon has
  no container filesystem and no archive endpoints.
- `jobs` has no completion callback; the desk reads job state on its frame tick. Docker emits no
  event for a file copy, so a copy-in's effect is not seen by the list refresh.

## Goals / Non-Goals

**Goals:**

- Copy out and copy in from the Files view on a container, one job each, with the safety rules
  in one tested package.
- Exact-name destinations, refusals on this machine, no-clobber, cleanup on failure.
- Keep the view's existing promises: no contents in the index, no sizes for added paths, no
  image rebuild.

**Non-Goals:**

- Any copy that does not start in the Files view on a container (no container-kind action, no
  CLI flag).
- Overwriting a local path, following symlinks, preserving ownership or xattrs, copying
  hard links as links (they are sent as regular files).
- Changing the Files view's display beyond the two keys and their job/refresh behaviour.

## Decisions

### W1 — The entry points and the two forms

`onFilesKey` gains `o` (out) and `i` (in), both only when `fs.kind == KindContainers`; on an
image they set the status sentence and open nothing. In read-only mode `i` is refused there,
before any form; `o` is not, because it only reads the daemon.

The forms use the existing field model:

| form | fields | prefilled |
| --- | --- | --- |
| copy out | `Local destination` | empty, placeholder `~/state.db` |
| copy in | `Local source`, `Container destination` | source empty, destination the cursor path |

The form body shows the container path (out) or the container and daemon (in), the container
path rule in one sentence ("the path is the destination itself; its parent must exist"), and
the skip/overwrite effects. Copy-in's Enter starts a local scan in a `tea.Cmd` (`formBusy`), so
a big tree does not freeze the desk; its answer opens the plan dialog.

*Alternative:* the create form's seven-field shape with the container path as a field on copy
out. Rejected: the cursor already names it, and an editable source in copy-out invites copying
something other than what is highlighted.

### W2 — `internal/transfer` owns the rules

A new package, with no UI and no engine, does the two host-side halves:

```go
// Scan counts what a copy-in would send, without opening a file.
func Scan(ctx context.Context, src string) (Tree, error)

// Tar writes src as a tar whose single top-level entry is name.
func Tar(ctx context.Context, w io.Writer, src, name string, p Progress) (Tree, error)

// Extract reads a tar whose single top-level entry is any name and writes it
// at dest, which must not exist.
func Extract(ctx context.Context, r io.Reader, dest string, p Progress) (Tree, error)
```

`Tree` is `{Entries, Files, Dirs, Bytes int, Skipped []string}`. Rules enforced here, tested with
temp directories:

- **Names.** An entry name is cleaned with `path.Clean`; empty, absolute, any `..` component, NUL
  or a name whose parent was created as a symlink is refused. The extractor tracks the symlinks
  it has made and refuses to write beneath one.
- **Order.** Directories and regular files first, symlinks and hard links last, so nothing is
  written through a link made by the same archive.
- **Types.** A symlink is created with `os.Symlink` and never followed; a hard link is validated
  to point inside the destination and recreated, or sent as a regular file when archiving. A
  character device, block device, FIFO or socket is never opened or recreated: it is counted in
  `Skipped` with its escaped name.
- **No-clobber.** `Extract` creates its destination itself: `os.OpenFile(O_CREATE|O_EXCL)` for a
  file, `os.Mkdir` for a directory. An existing path is an error, not an overwrite, and the
  check is atomic rather than a stat followed by a write.
- **Cleanup.** `Extract` removes what it created before returning an error; `ctx.Err()` is
  checked per entry, so cancelling the job cleans up too.
- **Modes.** Each created path gets the archive's permission bits with `os.Chmod` after
  creation (umask does not silently narrow them), for directories before their children.

*Alternative:* put this in `internal/files`. Rejected: `files` is the index and merge model; a
transfer needs the host filesystem and has an opposing job. Keeping it separate keeps the
index's "no contents" rule visible.

### W3 — The wire: exact names, in both directions

**Out.** `GET /containers/{id}/archive?path=<source>` answers a tar whose single top-level entry
is the source's base name, plus `X-Docker-Container-Path-Stat` (base64 JSON: name, size, mode,
mtime, link target). The desk checks the header's `Name` matches the base it asked for, extracts
with the top-level renamed to the local destination's base, and gets the mode and size from the
header for the confirmation and progress.

**In.** The desk sends `PUT /containers/{id}/archive?path=<parent(destination)>` with a tar
whose entries are rooted at `base(destination)`. The daemon extracts relative to `path`, so the
named destination is created or its same-named files replaced. `AllowOverwriteDirWithFile` stays
false and `CopyUIDGID` false (the daemon's ownership applies).

This is the deliberate divergence from `docker cp`: Docker nests a source inside an existing
destination directory; the desk's form says the path is the destination itself. The gain is a
confirmation that can state exactly what will be written, and a local "refuse if it exists" rule
that means something. The parent must exist in both directions; a missing parent is the daemon's
refusal (in) or the form's (out).

*Alternative:* hand the daemon the source base and let it apply `cp` nesting. Rejected: the
confirmation could no longer name the resulting path, and the local no-clobber rule would not
correspond to anything.

### W4 — Two engine methods, one of them a mutation

```go
// CopyFromContainer streams a tar of path and its stat. A GET.
CopyFromContainer(ctx context.Context, id, path string) (Archive, error)
// CopyToContainer extracts a tar at parent(dest). A PUT.
CopyToContainer(ctx context.Context, id, dest string, content io.Reader) error
```

`Archive` carries `io.ReadCloser` and the `engine`-owned stat (name, size, mode, link target),
not the client's type. `CopyFromContainer` is a read: it is added to `readMethods` and passes
through the read-only wrapper. `CopyToContainer` is a mutation: `readonly.go` overrides it and
the coverage test's count becomes 20 — the tripwire working as intended.

*Alternative:* one method with a direction flag. Rejected: it would make a read and a mutation
indistinguishable to the read-only wrapper and its test.

### W5 — Copy-out is a job, not a plan

It changes no daemon object, so it has no `plan.Plan` and no confirmation dialog: the form names
the container source, the absolute local path and its rule, and Enter starts the job. The job:

1. `CopyFromContainer` (the form has already refused an existing path and a missing parent).
2. `transfer.Extract` into the destination, reporting entries and bytes.
3. On error or cancellation, the extractor has removed what it wrote; the job reports the
   daemon's or the filesystem's message.

The job is fed by the frame tick like any other; `c` in the Jobs pane cancels it, and the
cancellation reaches the extractor's context. Progress is bytes written (from the stat's size
for a file) and entries for a directory, where no total is known before reading.

*Alternative:* a plain goroutine like export. Rejected: a directory can be hundreds of
megabytes, and the desk's one rule for long work is a job with progress and cancellation.

### W6 — Copy-in is a plan with one streamed step

`plan.CopyIn(container, spec)` where `spec` is `{Source, Dest string}`:

- `transfer.Scan` runs first. A missing source, a source that is the local root `/`, a source
  whose root is a special file, a destination of `/`, or a local permission error is returned as
  an error, so the form stays open with the reason. Nothing is a blocker: the daemon decides
  conflicts at the destination.
- On success the plan has one step, `OpCopyIn`, whose `Step.Copy` carries the local source, the
  container destination and the scan (entries, bytes). `Tier` is `TierConfirm`; the plan dialog
  names the daemon, container, absolute source, destination, entry count and bytes, and says
  same-named files are replaced. `y` confirms.
- `Execute`'s step opens the source and writes the tar into an `io.Pipe` while
  `eng.CopyToContainer` reads it, in one goroutine; the first error from either side is
  returned, the pipe is closed, and the job reports the entries and bytes it sent. There is no
  result to carry forward, no dependency edge.

*Alternative:* a job started straight from the form, with the confirmation done by the form
itself. Rejected: the project's confirmations for mutations are a separate, settled input
(`mutation-safety`), and a copy-in can replace files.

### W7 — The local path, the confirmation and the view afterwards

- The local path uses `report.ExpandPath` (so `~` and relative paths mean what the export
  dialog's mean) and `os.Lstat` for the existence and parent checks. The form shows the absolute
  path it will write before Enter; the extractor enforces it atomically.
- After a copy-in job starts, the view records its job ID in `filesState.copyJob` (with the
  container). On the frame tick, if that job has finished, the view clears it, calls
  `readChanges()` for that container and, on success, says in the status line that the change
  list was read again. The image's tree and index are untouched, so an added file appears with
  no size and a changed file keeps the image's size, exactly as the view already says.
- Copy-out starts no view work: it changes nothing the view shows.

*Alternative:* re-read the change list immediately when the job starts. Rejected: it would
usually run before the daemon has written anything.

### W8 — The fake daemon gets a container filesystem and both archive endpoints

`fakeengine.Container` gains a small in-memory tree (`map[string]*fileNode`: content, mode, link
target), seeded for the demo containers: `shop-web-1` gets `/etc/nginx/nginx.conf`,
`/usr/share/nginx/html/index.html`, `/var/lib/app/state.db`, a symlink and a FIFO, matching the
change list where it matters. The handlers:

- `GET /containers/{id}/archive?path=…`: resolve the path against the container's root (404
  `Could not find the file … in container …`); build a tar of the base and its subtree; set
  `X-Docker-Container-Path-Stat` to the base64 JSON stat. A file's stream is one entry; a
  directory's entries are `base/...`.
- `PUT /containers/{id}/archive?path=…`: the path must be an existing directory; untar with the
  same name rules as `transfer` (so the fake cannot be more permissive than the desk), replacing
  same-named entries; record the request for assertions.

`make crosscheck` gains a `docker cp` section: file out and in, a directory both ways, a symlink,
a missing path and a file-over-directory refusal — the real CLI against the fake, as the other
endpoints are checked.

*Alternative:* serve a real temp directory per container. Rejected: the fake's other objects are
in-memory and must be deterministic across goroutines; a temp dir adds lifecycle and cleanup.

### W9 — What the operator sees when entries are skipped or hostile

A skipped special file is not an error: the job lists it as `skipped <path>: not a regular file,
directory or symlink`, and the copy succeeds. A traversal-shaped or absolute entry is an error:
the copy stops, the partial destination is removed, and the job names the entry. That is the
difference between data that cannot be represented and an archive that is trying to escape.

### W10 — Ownership, modes and what is not preserved

Out: the extractor sets the archive's permission bits and does not chown; files belong to the
operator running the desk. In: the tar's headers carry the local permission bits and uid/gid 0;
the daemon's own extraction policy decides ownership, as `docker cp`'s does. Timestamps are not
restored on either side, and extended attributes, ACLs and sparse files are not represented.
The forms say modes and contents, not ownership.

## Risks / Trade-offs

- **The daemon's extraction root must be what W3 assumes** (entries resolved under `path`) →
  an opt-in real-daemon test copies in and out and asserts the exact resulting paths; the fake
  mirrors the assumption, and the crosscheck keeps the fake honest with the real CLI. If it
  differs, only `CopyToContainer`'s request shape changes.
- **A huge local tree makes the confirmation slow** → the scan runs off the UI goroutine with
  `formBusy` and is cancellable by Escape; the job re-walks while streaming, so no tree is kept.
- **A local path can be created between the check and the write** → O_EXCL/Mkdir decide, and the
  error says the path exists; nothing is replaced.
- **A source can change between the scan and the job** → the confirmation says the source is
  read when the job runs; the job's counts may differ from the scan's, and it reports what it
  actually sent.
- **A running container's writes race the copy** → the daemon streams what it sees; the job
  reports the bytes it got. `docker cp` has the same property.
- **Symlinks could be used to escape the destination** → links are created last and entries
  beneath a created link are refused; both directions have tests with hostile archives.
- **No event follows a copy-in** → the frame-tick job watch re-reads the change list; if the
  view was closed, nothing is fetched.
- **Ownership surprises** → stated in the confirmation and in the README: the daemon's default
  applies, and the desk never chowns.

## Migration Plan

No stored state, config or wire format changes. Rolling back is a revert, and files copied in or
out are ordinary files at the paths the operator named.
