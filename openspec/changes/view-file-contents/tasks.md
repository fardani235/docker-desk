# Tasks

## 1. Bounded preview

- [x] 1.1 Add `files.ReadPreview(ctx, r, size)` with the 1 MiB, 10,000-line and 2 KiB-line bounds, UTF-8/NUL binary classification, control/ANSI escaping and cut flags. Verify pure tests for text, an empty file, a 3 MiB file, 20,000 lines, a 3 KiB line, NUL, invalid UTF-8, a cancellation and an exact-size read.

## 2. The reader in the Files view

- [x] 2.1 Route `Enter` on a regular file in a container to the reader, before the image-archive branch; refuse image files, directories, symlinks, hard links, devices, FIFOs, sockets, deleted paths and unknown types with their type; open an added regular file even when the image tree is not built. Verify model tests for each and that no request is sent before Enter.
- [x] 2.2 Add the read job: `CopyFromContainer` for exactly the selected path, one tar entry whose name is the path's base and whose type is regular, then `files.ReadPreview`; report bytes and bounds, close the stream, cancel cleanly, and log no content. Verify tests for one request, wrong entry/name, directory answer, 404, cancellation, read-only and a job log without file bytes.
- [x] 2.3 Add the full-screen reader state and box: loading/progress, metadata (path, container, type, size, mode, owner, time), source/current-filesystem note, line count and cuts, text or binary-metadata body, scroll/wrap, `Esc`/`q` close with the Files state restored. Verify render tests for each and that the Files cursor, expansion, order and filter are unchanged after closing.
- [x] 2.4 Make the reader consume input and mouse: every key and wheel notch goes to the reader; the hit map records no tree rows behind it; wheel scrolls, and a click reaches nothing. Verify key and mouse tests with the Files view underneath.

## 3. Safety and retention

- [x] 3.1 Prove the content boundary: image files send no archive request, the only container request is the archive `GET`, the content is absent from the job log and export, and it is discarded on close. Verify focused tests for each.
- [x] 3.2 Verify escaping end to end: a file holding ANSI escapes, NUL and invalid UTF-8 yields only visible escapes (or binary metadata) and never a raw control sequence; a hostile path is still shown escaped.

## 4. Documentation, end to end, and integration

- [ ] 4.1 Update the README's Files section and its "No file contents" wording, the key lists and "What it does not do"; add the reader's bounds, binary rule, container-only source and image refusal. Record the decision in `docs/DECISIONS.md`, update `docs/TESTING.md`, and regenerate the Files screen captures.
- [ ] 4.2 Add pty tests: open Files on a container, select a regular file, Enter, and see its text with metadata; a binary file shows metadata only; an image file refuses without a request; read-only views the same bytes; a cancel leaves the tree. Capture the new screen.
- [ ] 4.3 Run `go test ./...`, `go test -race ./...` and `make e2e`; verify the full suite, the race run and the captures are green.

## Workflow follow-up

- Archive the change after review.
- Validate the archived change and the updated `files-explorer` spec.
