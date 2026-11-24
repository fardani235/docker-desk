# Tasks

## 1. The streaming writer

- [ ] 1.1 Refactor `report.WriteFile` onto an unexported `writeFile(path, force, write func(io.Writer) error)` and add `report.WriteStream(path, write)` with no force. Verify all existing `WriteFile` tests still pass and new tests cover: success at mode 0600, callback error leaves no destination and no temporary file, an existing path is refused, a folder is not created, and a file appearing during the write is not replaced.

## 2. The save job

- [ ] 2.1 Add the Images `w` action and the save state (image ID, reference, name, platform, listed size). Resolve the platform from the loaded detail or one `InspectImage` command (bounded, generation- and sequence-guarded), with a status line while it reads. Verify model tests: `w` sends no request until the form's Enter; a missing detail triggers exactly one inspect; a failed inspect opens nothing.
- [ ] 2.2 Implement `submitSaveImage` and the job: validate the path (`report.ExpandPath`, existing path/folder rules), re-inspect and refuse a moved tag, take the platform from the fresh inspect, call `SaveImage` by reference or ID, stream in chunks through `report.WriteStream`, and report bytes, path and platform. Verify a fake-engine test asserts one `GET /images/get` with the reference and platform, the file's bytes match the fixture, and the job log names both reference and ID.
- [ ] 2.3 Handle cancellation and failure: close the daemon stream, remove the temporary file, leave nothing at the destination, and report the reason (daemon, disk, moved tag, path). Verify tests for cancel mid-stream (fake `ExportChunkDelay`), a write failure, a moved tag, and a daemon error.

## 3. The form

- [ ] 3.1 Add `formSaveImage`: one path row, prefilled `~/docker-desk-image-<slug>-<YYYYMMDD>.tar` for a tagged and an untagged row, with the image, short ID, reference/ID, platform, tar/0600/no-replace notes and no invented file size. Verify render tests for both rows and the default path.
- [ ] 3.2 Validate the path in the form before the job: `~` expansion, a missing folder, a directory, an existing file, and a symlink each keep the form open with the right sentence and send nothing. Verify one test per case.
- [ ] 3.3 Prove the form is bound to its image: the highlight moving after it opens still saves the named image (or reports it gone), a multi-selection refuses with the one-at-a-time sentence, and read-only starts the same save. Verify model tests for each.

## 4. Documentation, end to end, and integration

- [ ] 4.1 Update the README's Images walkthrough, its key list and its export/file-writing section with `w save archive…`, the pinned tag/platform, the path rules and read-only behavior; regenerate any affected Images screen captures and verify the diff is intended.
- [ ] 4.2 Add pty tests: `w`, Enter on the prefilled form, wait for the job, and open the written tar to confirm it is the fixture; a read-only run saves the same bytes; a cancel leaves no file. Capture the new screen.
- [ ] 4.3 Run `go test ./...`, `go test -race ./...` and `make e2e`; verify the full suite, the race run and the captures are green and the existing exports are unchanged.

## Workflow follow-up

- Archive the change after review.
- Validate the archived change and the new capability.
