# Tasks

## 1. The transfer package

- [x] 1.1 Create `internal/transfer` with `Tree`, `Progress` and `Scan(ctx, src)`; verify `go test ./internal/transfer` counts files, directories, bytes and skips for a temp tree, and refuses a special-file root.
- [x] 1.2 Implement `Tar(ctx, w, src, name, p)` writing file, directory and symlink entries with modes and long names (PAX), streaming regular files and never following a symlink; verify a round trip through `archive/tar` reproduces the tree and the header names are rooted at `name`.
- [x] 1.3 Implement `Extract(ctx, r, dest, p)` with no-clobber creation (O_EXCL file, Mkdir directory), modes applied with `Chmod`, links created last and children of a created link refused; verify a file and a directory extract to exactly `dest` and an existing `dest` is refused.
- [x] 1.4 Enforce the hostile-archive rules: absolute names, `..` components and NUL are refused; devices, FIFOs and sockets are skipped and named; a hard link is recreated inside the destination or refused; verify each with table-driven tests and assert nothing outside a temp root was created.
- [x] 1.5 Verify cleanup: a cancelled context and an injected write error both remove everything the extractor created, with a test that walks the parent before and after.

## 2. Engine and the fake daemon

- [x] 2.1 Add `engine.Archive` (stream plus owned stat) and `CopyFromContainer(ctx, id, path)`; verify an engine test against the fake returns the stream, the stat's name and size, and that a missing path gives a `KindNotFound` error.
- [x] 2.2 Add `CopyToContainer(ctx, id, dest, content)` sending `path=parent(dest)`, `AllowOverwriteDirWithFile=false`, `CopyUIDGID=false`; verify an engine test asserts the query and the tar the fake received, and that a missing parent is the daemon's error.
- [x] 2.3 Classify the methods for read-only: `CopyFromContainer` in `readMethods`, a `readonly.go` override for `CopyToContainer`, and the coverage count raised to 20; verify `go test ./internal/engine -run TestReadOnly` passes and a read-only copy-out still streams.
- [x] 2.4 Give `fakeengine.Container` an in-memory filesystem with seeded files, a symlink and a FIFO, and implement `GET /containers/{id}/archive` with the `X-Docker-Container-Path-Stat` header; verify `go test ./internal/fakeengine` covers a file, a directory, a symlink and a missing path.
- [x] 2.5 Implement `PUT /containers/{id}/archive` in the fake with the same name and traversal rules, replacing same-named entries and refusing a missing destination directory; verify its tests and that `Requests()` records the upload.
- [x] 2.6 Add a `docker cp` section to `scripts/cli-crosscheck.sh` (file and directory both ways, a symlink, a missing path, a file over a directory) and run `make crosscheck` on a machine with the `docker` CLI.

## 3. The copy-in plan

- [x] 3.1 Add `OpCopyIn`, `Step.Copy` (local source, container destination, scan stats) and `plan.CopyIn(container, spec)`; return errors for a missing source, a source of `/`, a special-file root, a destination of `/` and a local permission error; verify plan tests for each, with no steps built.
- [x] 3.2 Implement the step in `executeStep`: open the source, write the tar into an `io.Pipe` in a goroutine while `CopyToContainer` reads it, return the first error, and report entries and bytes through `run`; verify a plan test copies a temp tree into the fake and the fake's tree matches, and a failing upload surfaces the daemon's message.
- [x] 3.3 Verify `describe()` and the job's title, target and output name the container, source and destination and never a file's contents; add a test with a file whose content is a sentinel.

## 4. The Files view

- [x] 4.1 Add `i` and `o` to `onFilesKey` for containers only, with the image status sentence and the read-only refusal for `i`; verify UI tests for each key, an image, and read-only sending nothing.
- [x] 4.2 Add the two forms (`formCopyOut` one path field; `formCopyIn` source and destination fields, destination prefilled from the cursor) with the container path, daemon and rule in the body; verify a UI test reads the prefilled destination and the rule sentence.
- [x] 4.3 Resolve the local path with `report.ExpandPath` and check it with `os.Lstat` on Enter: refuse an existing destination and a missing parent with the reason in the form, and show the absolute path before submitting; verify tests for both refusals and for `~/x` and a relative path.
- [x] 4.4 Start copy-out as a job that streams, extracts and cleans up on cancel; verify a UI test copies a seeded file and directory out through the fake and that cancelling leaves no path behind.
- [x] 4.5 Wire copy-in: scan in a `tea.Cmd` with `formBusy`, open the plan dialog on success (keeping the form open with the error on failure), and confirm with `y`; verify the confirmation names the daemon, container, source, destination, entries and bytes, and that Enter or `n` sends nothing.
- [x] 4.6 Re-read the change list when the copy-in job finishes: record the job in `filesState`, check it on the frame tick, and call `readChanges()` once; verify a UI test sees the copied path marked added without an archive read, and that closing the view stops the watch.
- [x] 4.7 Add `i`/`o` to the Files footer and help, and extend `TestFilesHelpAndFooter` to require them; verify `go test ./internal/ui -run FilesHelp`.

## 5. Documentation and the captured screen

- [x] 5.1 Record the decisions as W1–W10 in `docs/DECISIONS.md` (round seventeen) with their alternatives and costs, matching `design.md`; verify the headings are sequential and the round's intro states what was left out.
- [x] 5.2 Document the two keys, the exact-name rule, the local refusals, the skipped special files and the ownership note in the README's Files section; verify the documented keys match the help text and the documentation test passes.
- [x] 5.3 Capture the copy-in form and its confirmation into `docs/screens/` following the existing captures; verify the files render and the README link resolves.
- [x] 5.4 Add the copy flow to `docs/TESTING.md`'s proof table (transfer tests, the fake, the UI model, the CLI cross-check, the opt-in real-daemon test); verify the commands named there are the ones the suites use.

## 6. Integration

- [x] 6.1 Add an end-to-end test that opens a container's Files view, copies a seeded file out, and asserts the local file's bytes and mode; verify `go test ./internal/e2e -run Copy`.
- [x] 6.2 Add an end-to-end test that copies a local directory in, asserts the fake's tree and that the change list is read again, and a case where an existing local destination is refused before any archive request; verify the fake's request log.
- [ ] 6.3 Add the opt-in real-daemon test for the wire semantics (copy a file out and back in, assert the exact paths and modes), guarded like the other real-daemon tests; verify it skips without a daemon and passes with one.
- [x] 6.4 Run `make build`, `make vet`, `make crosscheck` and `go test ./...`, and resolve every regression; record anything the suite reports that predates this change instead of silently accepting it.

## Workflow follow-up

- Archive the change after the project's review requirements are satisfied.
- Verify the archived specs and the main capability specs agree after archive.
