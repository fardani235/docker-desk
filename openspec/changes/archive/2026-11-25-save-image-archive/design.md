# Design

## Context

`engine.SaveImage` already streams `GET /images/get` for one image and platform; Files and
Findings use it. The desk also has a single, strict file writer (`report.WriteFile`): mode 0600,
no replacement, folder must exist, hidden temporary file beside the target, published by a hard
link so nothing that appears meanwhile is overwritten, and removed on any failure.

The image list already carries the reference (`row.imageRef`), the image ID and the name; the
Details load has the inspected platform, but a save must not depend on the details having
loaded. Saving is a read: `SaveImage` is a `GET`, so the action belongs in read-only mode too.

See `proposal.md` for motivation and the two spec deltas for required behavior.

## Goals / Non-Goals

**Goals:**

- Save the highlighted image with the same fidelity the desk shows: the platform from inspect
  and the tag when the row has one.
- Reuse the existing file rules through one streaming writer, so the desk still has one place
  that writes files.
- Stream: neither the archive nor the image is buffered in memory, and the UI stays live.
- Leave nothing behind on cancel or failure, and never replace a file.

**Non-Goals:**

- Saving several images at once, or a selection as one tar.
- Loading, importing, tagging or pushing an archive.
- A command-line `--save`. The action is the Images view's.
- Choosing a platform other than the image's own.
- Compressing the tar or inventing a size before it is written.

## Decisions

### D1 — `w` is an Images action, and saving is not a mutation

`KindImages` gains `action{key: "w", label: "save archive…", desc: "write this image's archive
(docker save) to one local file; the tag is kept", needsRow: true}`. `mutating` stays false so
read-only does not refuse it, and the existing one-row rule refuses a multi-selection with its
usual sentence. The action menu lists it and reaches it from List and Details; the direct key
is the List pane's, because `w` already means wrap on the Details text tabs and export on
Findings.

**Alternative rejected.** A global key would collide with `w` export in Findings, Packages,
Space and Files. A CLI flag is a different surface the user did not ask for.

### D2 — The platform is resolved before the form, never guessed

`openSaveArchive` captures the row (ID, reference, name). If the loaded `ImageDetail` is for
that ID it uses it; otherwise it runs one `InspectImage` in a command (10 s bound, tagged with
the focus generation and a save sequence), shows "reading the image's platform…" in the status
line, and opens the form when the answer arrives. A failed inspect opens nothing and reports
the daemon's message. The form is therefore always able to name the platform and refuses to
exist while the platform is unknown.

If the image disappears before the inspect answers, the status says so; nothing is written.

### D3 — A tagged row is saved by its reference, pinned; an untagged one by ID

The form records the row: `imageID`, `imageRef` (possibly empty), `name`, `platform`, and the
daemon's listed size. It is bound to that row even if the highlight moves.

In the job, immediately before streaming:

1. inspect the reference (or ID) again;
2. if it no longer names the recorded image ID, write nothing and fail with "the tag moved";
3. take the platform from that inspect, and refuse if it is not the one the form showed;
4. call `SaveImage(ctx, ref, platform)` when the row had a reference, or `SaveImage(ctx, id,
   platform)` when it did not, so a tagged archive keeps its tag and an untagged one loads
   untagged.

The inspect→save gap is the same narrow race `checkImagePin` accepts for removals: the daemon
has no atomic "save this exact ID under this tag" call. The job names the reference and the
short ID in its first line, so the operator can see what was asked for.

### D4 — One streaming writer in `report`, shared with the exports

`report` gains:

```go
// WriteStream writes what write puts into w to path under the same rules
// WriteFile follows: 0600, no replacement, folder must exist, hidden
// temporary file, published only when complete, removed on failure.
func WriteStream(path string, write func(io.Writer) error) error
```

`WriteFile` becomes a caller of an unexported `writeFile(path, force, write)`, so both paths
share the precheck, temporary file, `Chmod(0600)`, `Sync`, hard-link publication and cleanup.
`WriteStream` never forces; the TUI has no force. The package comment's "one place the desk
writes a file" stays true.

**Alternative rejected.** Buffering the archive to reuse `WriteFile` would put gigabytes in
memory and defeat the stream the desk already has. A second writer in `transfer` would fork
the no-replace/cleanup rules and their tests.

### D5 — The job streams, reports bytes and cleans up

`submitSaveImage` validates the path (`report.ExpandPath`, then `Lstat`: no existing path, no
directory, the folder exists) and starts a job. The job:

- re-inspects and pins as D3;
- calls `SaveImage` and reads its body in 32 KiB chunks, checking `ctx` between reads and
  writing through the `WriteStream` callback;
- reports `wrote <bytes>` about every 100 ms while the size is unknown (there is no
  Content-Length to trust), then the final bytes, path, platform and whether the archive is
  tagged or untagged;
- on cancel returns `ctx.Err` after closing the body; `WriteStream` removes the temporary file;
- on any error logs "nothing was written on this machine", the reason and the removed
  temporary;
- logs that the daemon was only read (`GET`), and in read-only mode says so explicitly.

No final path is created until the stream is complete; a file that appears at the destination
meanwhile makes the publication fail without replacement.

### D6 — The form is the confirmation, and it shows no invented size

The form has one path row, prefilled
`~/docker-desk-image-<slug(ref or ID)>-<YYYYMMDD>.tar` via `report.Slug`. It states the image,
short ID, reference/ID, platform, that the archive is a tar, that the file is written mode
0600 and never replaces one, that the folder must exist, and that nothing on the daemon changes.
It does not state a file size: the daemon's listed image size is compressed and the tar is not.
Enter starts the job; Escape cancels; the existing form arming rule applies. The form is the
confirmation, as the copy-out form is for the same kind of local write.

### D7 — The fake serves the stream, and the tests read the tar

The fake's `GET /images/get` already streams a fixture archive per reference and records the
request (including the platform query). Tests use `ExportChunkDelay` to keep a save in flight
for cancellation and `SetImageArchive` for exact bytes; the pty test saves an image and opens
the resulting tar to confirm it is the fixture, not just a file of the right size.

## Risks / Trade-offs

- **Tag moves between the pin and the stream** → The inspect is immediately before the save and
  the job names both reference and ID; the daemon offers no atomic save-by-ID-under-tag. This
  matches the existing removal pin's residual window.
- **Disk fills mid-stream** → The write error fails the job, `WriteStream` removes the temporary
  file, and nothing appears at the destination.
- **Very large archives or slow remote streams** → Streaming keeps memory flat and the desk
  live; bytes-written progress is shown, and cancel closes the stream and deletes the partial.
- **The daemon lists a size that is not the tar's size** → The form refuses to quote a file
  size; only the bytes actually written are reported.
- **Platform not supported by an old daemon** → `SaveImage` already omits the platform parameter
  for older API versions, and the archive is the daemon's own.
- **The destination's precheck and the final write are not atomic** → The precheck is only for
  a friendly form message; `WriteStream`'s hard-link publication is the real rule.

## Migration Plan

None. The action is additive and read-only-safe; reverting removes the key, the form, the job
and `WriteStream`. No stored data, API shape or existing export changes.
