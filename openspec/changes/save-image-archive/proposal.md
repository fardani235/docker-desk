# Proposal

## Why

The desk can read an image's archive for Files and Findings, and it can write evidence and
file listings to disk, but it cannot hand the operator the archive itself. Getting an image off
a remote daemon today means leaving the desk and running `docker save` by hand — against
whichever daemon the desk is pointed at, with none of the desk's path rules. A saved image is
the one artifact that moves a build to another machine, so the read the desk already performs
should be able to end in a file.

## What Changes

- Images gains `w save archive…` on the highlighted row (direct key and action menu entry).
  It is a read: saving is allowed in read-only mode.
- A form names the image, its short ID, the platform the desk will save, the prefilled local
  path and the fact that the daemon is only read. Enter starts the save.
- The archive is streamed from `GET /images/get` to a hidden temporary file beside the
  destination and published under the chosen name only when complete. It is created mode 0600;
  an existing path is never replaced, a folder is never created and a directory is refused.
- The image is saved by its reference when the row has one, after re-inspecting that the
  reference still names the shown image, so the archive carries the tag; an untagged row is
  saved by ID. The platform is the one the image is inspected as (the Files/Findings platform)
  and is stated in the form.
- The save runs as a cancellable job: bytes written are reported, the daemon stream is closed
  and the temporary file removed on cancel or failure, and the job reports the final bytes,
  path and platform.
- The path rules are shared with the existing exports through one streaming writer, so the
  desk still has one place where it writes a file.

## Capabilities

### New Capabilities

- `image-archive-save`: saving one highlighted image's archive to a local file — the action,
  the form, the pinned reference and platform, the streaming path, progress, cancellation and
  cleanup.

### Modified Capabilities

- `mutation-safety`: adds the local-write rule for an image archive: one named path, never
  replaced, nothing changed on the daemon, and a partial file removed on cancel or failure.

## Impact

- `internal/report`: a streaming writer sharing the existing temporary-file/link rules with
  `WriteFile`, and tests proving the same no-replace and cleanup behavior.
- `internal/engine` and `internal/fakeengine`: no new endpoint; the existing `SaveImage` and
  image inspect are used, and the fake's archive is the test stream.
- `internal/ui`: the `w` action, an inspection step when the platform is not loaded, a save
  form, a cancellable job, path validation and progress, plus help/hints.
- `README.md` and the Images walkthrough; no new flag and no new dependency.
- Tests: writer rules, the pinned reference/moved tag, platform selection, cancel cleanup, the
  form's path errors, read-only, and an end-to-end pty save whose tar is checked.
