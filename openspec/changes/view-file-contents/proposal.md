# Proposal

## Why

The Files view can show an image's or container's tree — every path with its type, size, mode
and layer — but it deliberately never reads what a file holds. Answering "what is in this
file?" therefore means leaving the desk for `docker cp` or a saved archive. On a container the
daemon already offers one cheap `GET` for exactly one path, so the desk can answer without
leaving and without touching anything.

## What Changes

- In a container's Files view, `Enter` on a regular file opens a full-screen reader. It asks the
  daemon for that one path (`GET /containers/{id}/archive`), shows progress, and can be
  cancelled; nothing is sent until Enter, and it works in read-only mode.
- The reader shows up to 1 MiB, 10,000 lines, with a line cut at 2 KiB; anything cut is said
  plainly. Control characters, ANSI escapes and invalid bytes are escaped so file content can
  never drive the terminal.
- Only text is shown: a file that is not valid UTF-8 text, or has NUL bytes, gets its metadata
  (path, type, size, mode, owner, time, source) and no contents. No hex view.
- Image files are not read. The desk says so, and points to the container view or saving the
  archive (`w`); the image archive is never streamed again for file contents.
- Symlinks, hard links, devices, FIFOs, sockets, directories and paths the container deleted are
  never followed or opened; the reader says what the entry is instead. A changed path's
  contents come from the container's current filesystem, and the reader says the tree's size is
  the image's.
- What is read stays in memory while the reader is open and is never written, exported,
  searched, logged or kept after it closes. The index, filter and listing export continue to
  hold names and metadata only.
- The reader is an overlay: `Esc` returns to the Files view exactly as it was, and while it is
  open it consumes every key and wheel notch so nothing reaches the tree behind it.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `files-explorer`: the "no file contents, ever" rule becomes "contents only on request, from a
  container, bounded and never kept"; adds the reader's entry, limits, text/binary rules,
  escaping, source notes, closing and failures.

## Impact

- `internal/ui/files.go` and a new reader state/box: `Enter` routing, the archive job, the
  overlay, keys and hints, and pointer consumption.
- `internal/files` (or a small pure helper): text/binary classification, control-character
  escaping, line/byte/line-length bounds, and their tests.
- `internal/engine`: no new method; `CopyFromContainer` is reused as a `GET`.
- `README.md` (Files section, "What it does not do"), `docs/DECISIONS.md`, `docs/TESTING.md`
  and the Files screen captures.
- Tests: pure bounds/classification, the container archive read (one request, only that path),
  image refusal, symlink/device/deleted refusal, truncation/binary metadata, cancel/failure,
  read-only, and an end-to-end pty view.
