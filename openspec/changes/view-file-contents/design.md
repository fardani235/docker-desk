# Design

## Context

The Files view already holds every path's type, size, mode, owner, layer and container change,
built from an image archive's headers and the container's change list. It deliberately never
reads a file's bytes: the index, filter and export are metadata only, and the only content read
the desk performs is a copy-out. A container path, however, is readable in one request —
`CopyFromContainer` is `GET /containers/{id}/archive?path=…` and returns a tar with exactly that
entry — while an image path has no per-file endpoint at all.

See `proposal.md` for motivation and the `files-explorer` delta for behavior.

## Goals / Non-Goals

**Goals:**

- Answer "what is in this file?" for a regular file in a container without leaving the desk or
  writing anything to disk.
- Keep the existing guarantee for images and for every non-regular entry: nothing is followed or
  opened.
- Make untrusted bytes inert on screen: the viewer escapes controls and never lets content
  drive the terminal.

**Non-Goals:**

- Reading image files (that would need a second full archive stream).
- Hex dumps, editors, follow-symlink, or content search/export.
- Caching contents beyond the open reader; a container's files can change.
- Reading devices, FIFOs, sockets, directories, links or deleted paths.

## Decisions

### D1 — `Enter` on a regular container file opens the reader; nothing else changes

The existing Enter order in `onFilesKey` becomes:

1. on a container, a regular file row → open the reader for that path (one archive GET);
2. otherwise, if the image's tree has not been built → start the archive read as today;
3. a directory → expand as today;
4. an image's regular file → say image files are not read, and send nothing;
5. a symlink, hard link, device, FIFO, socket, deleted path or unknown type → say what it is,
   and send nothing.

The reader opens immediately in a loading state, so a slow container does not freeze the view;
the job reports bytes and can be cancelled with `c`, `Esc` or the Jobs pane.

**Alternative rejected.** A new `v` key would leave Enter's "open this" meaning inconsistent
and would be undiscoverable for the one thing a file row most obviously does.

### D2 — One container archive read, verified against the entry the tree holds

The job calls `CopyFromContainer(ctx, containerID, path)` and reads the tar. It accepts exactly
one entry whose name is `base(path)` and whose type is regular; a directory, link or different
name is refused with what was received. The index's type and change mark are checked before the
request, so a symlink is never followed even if the daemon would follow it; the header is
checked again after. The read is a `GET`, allowed in read-only mode, and no other request is
sent. Contents are what the container's filesystem holds now, not a point-in-time snapshot;
the reader says so.

`CopyFromContainer` is already used by copy-out; the reader does not extract to disk, it reads
the single entry into the bounded preview (D3).

### D3 — A pure bounded preview in `internal/files`

A new pure function (package `files`, no UI imports) turns the tar entry into what the reader
shows:

```go
type Preview struct {
    Text     []string // escaped, wrapped-ready lines; nil when binary
    Binary   bool
    Size     int64  // the header's size
    Read     int64  // bytes read
    Lines    int
    CutBytes bool   // the 1 MiB bound
    CutLines bool   // the 10,000-line bound
    CutLine  bool   // a line over 2 KiB
}
func ReadPreview(ctx context.Context, r io.Reader, size int64) (Preview, error)
```

Rules:

- read at most 1 MiB; a larger file is marked `CutBytes` and the stream may be closed;
- keep at most 10,000 lines, then `CutLines`;
- a line over 2 KiB is kept to 2 KiB, `CutLine`, and the rest of that line is discarded;
- text means valid UTF-8 with no NUL bytes; anything else is `Binary` and carries metadata only,
  no bytes, no hex;
- text is made inert: ESC and every C0/C1 control becomes a visible `\xNN`, CR is shown as `\r`
  or normalised, tabs are expanded; no ANSI sequence reaches the screen;
- every bound is reported; no figure is called the file's full size unless the header's size
  says so.

**Alternative rejected.** Putting this in the UI would make the bounds and escaping untestable
without a model, and a hex view was explicitly not wanted.

### D4 — The reader is a full-screen overlay inside Files

`filesReader` state holds the target (path, container, type, change, index size, mode, owner,
time), the running job, the preview, scroll/wrap and error. It renders as a full-screen box in
place of `filesBox`, with the Files view untouched underneath; `Esc` or `q` closes it and
restores the tree cursor, expansion, order and filter exactly. The `textView` machinery draws
the wrapped lines and scrolls; `w` toggles wrap; the header states the source, the container's
current-filesystem caveat, line count and every cut.

While the reader is open every key and wheel notch belongs to it. In the mouse hit map the
reader records its box only (no tree rows behind it), so a click cannot act on the tree; the
wheel scrolls the reader. The help overlay and the Files hints list the reader's keys.

### D5 — Nothing is kept, logged or written

Content lives only in the reader's memory and is dropped on close. The job logs path, bytes,
lines and bounds — never a line of content. No export, search, clipboard or write path sees it.
The image's archive is never requested for contents; an image file says to use a container or
save the archive (`w`, the image-archive save).

### D6 — Failures are reported without claiming content

A 404/deleted path, a daemon refusal, a wrong entry type/name, a cancelled read or a vanished
container leaves the reader with the reason and no content. The stream is always closed, and
the Files tree is unchanged. If the container changes during the read, the reader says the
contents are as the daemon read them, not a snapshot.

## Risks / Trade-offs

- **A container's file changes while it is read** → The reader states the source is the
  container's current filesystem; no snapshot is claimed.
- **A 1 MiB / 10,000-line / 2 KiB cut hides the answer** → Every bound is named on screen, and a
  copy-out remains available for the full file.
- **Binary detection is a heuristic** → The rule is explicit (NUL or invalid UTF-8); a false
  binary still shows the metadata and the reason, never a corrupted terminal.
- **A malicious file tries to drive the terminal** → All controls and ANSI escapes are escaped
  before drawing; content never reaches a raw writer.
- **The daemon returns a link or directory despite the tree** → The entry type and name are
  verified; nothing is followed or drawn.
- **A big archive read for a file** → None: images are refused, and a container read is one
  entry; the 1 MiB bound stops the read early.

## Migration Plan

None. The reader is additive and read-only-safe; reverting removes the state, the box, Enter's
file branch and the pure preview. The index, exports and copy paths are unchanged.
