# Tasks

## 1. The archive walk, in files mode

- [x] 1.1 Add `Mode` to `forensics.Options` — `ModeFindings` as the zero value (today's behaviour), `ModeFiles`, `ModeBoth` — and in `ModeFiles` read headers only: no rule evaluation, no secret scanning, no package-database capture; verify the existing analysis tests pass unchanged and a new test over a fixture whose files carry a secret and a dpkg status yields no finding and no package
- [x] 1.2 Append one entry per tar header in `ModeFiles` and `ModeBoth` — absolute cleaned path, type flag, mode, size, uid, gid, mtime and link target — per layer; verify a three-layer fixture with a replaced file, a whiteout and a symlink yields exactly the expected entries per layer
- [x] 1.3 Enforce the three bounds — 200,000 entries, 4 KiB a path, 48 MiB of path text — counting what each bound dropped after it is hit; verify with a test that sets each cap to a tiny value and sees the three counts, and with a fixture whose path is 4 KiB + 1 byte
- [x] 1.4 Expose the layer-to-instruction map the rules already build (`instr` in `rules.go`) on the report, 1-based like the Packages view; verify a 12-layer fixture whose layer 7 has a `RUN` line shows that instruction for layer 7 and `unknown` for a layer whose history entry is empty
- [x] 1.5 In `ModeFiles`, a zstd layer yields no entries and is named as unread; verify a fixture with a zstd layer produces an index whose header names it and whose other layers are complete
- [x] 1.6 Record 1.1–1.5 in `docs/DECISIONS.md` under a new `## Round fifteen — the Files view`, one entry per decision with the alternative it rejected; verify the section names `ModeFindings` as the zero value and gives the reason a second parser was refused

## 2. The index model, pure

- [x] 2.1 Add `internal/files` with `Merge(layers)`: later layers win, a whiteout removes the path, an opaque directory clears everything under it, a path keeps the 1-based layer that last wrote it, and the merge counts the paths removed by later layers; verify table tests for replace, remove, opaque, a directory written by several layers and a hard link, and that the removed count equals the whiteouts applied to paths the tree held
- [x] 2.2 Compute directory sizes and descendant file counts, and mark a directory's size as computed; verify a test where three files under a directory add up to the shown size and the count matches
- [x] 2.3 Add `Flatten(index, expanded, filter, order)` returning only visible rows, with `up`/`down`/page/top/bottom movement and the cursor keyed by path; verify the cursor stays on the same path across a re-order and a collapse of its parent
- [x] 2.4 Add the filter: case-insensitive substring on the full path, every match shown with its ancestor directories, the count of matches, and a flag that the index was cut; verify a deep match shows its ancestors and a no-match answer names the number searched
- [x] 2.5 Add `Escape`: control characters and `DEL` as `\xNN`, a newline as `\n`, invalid UTF-8 byte by byte; verify a table whose names contain ESC, CR, LF and the bytes `\xff\xfe`, and assert no raw ESC or newline is left in the output
- [x] 2.6 Add `Apply(index, changes)` marking added, changed and deleted, with the counts of changes outside the index and of a deleted path the index does not hold; verify tests for each kind, a change path that cleans above the root, and a cut index
- [x] 2.7 Add the view-state totals — entries kept of entries seen, each bound's dropped count, the paths removed by later layers, and the tree's own total beside the daemon's size with the sentence that they measure different things; verify a test at 200,001 entries, one with a dropped long path, and one where the two totals disagree and both are shown labelled
- [x] 2.8 Record 2.1–2.7 in the round-fifteen section of `docs/DECISIONS.md`; verify the entries name the rejected alternative for the merge order, the bounds and the escaping

## 3. The container's changes

- [x] 3.1 Add `engine.Change{Path string, Kind ChangeKind}` and `ContainerChanges(ctx, id)` to the `Engine` interface, implemented over the client's `ContainerDiff`; verify against the fake that one `GET /containers/{id}/changes` is sent and kinds 0, 1 and 2 decode as changed, added and deleted
- [x] 3.2 Seed a change list per container in `fakeengine` and serve the endpoint; give the demo one container with a file added, one changed, one deleted and one added directory; verify `engine_test.go` reads them and `make crosscheck` shows `docker diff` agreeing with the fake
- [x] 3.3 Confirm the call is allowed in read-only mode and appears in the request log as a `GET`; verify a test opens Files on a container in read-only mode and the log holds no request other than `GET`s
- [x] 3.4 Record the container-side decision in the round-fifteen section — why `ContainerDiff` and not `CopyFromContainer` — and name the deferred archive round; verify the entry states the cost difference in bytes

## 4. The Files view

- [x] 4.1 Add `filesState` to the model, `F` opening it when the highlighted kind is Images or Containers, and `Esc` restoring focus, kind and highlight exactly; verify behaviour tests for both kinds and for `F` on a volume, which yields one status sentence and no view
- [x] 4.2 Draw the opening facts — identity, platform, layer count, the daemon's size — and "nothing has been read"; wire `Enter` to one job that reads the archive and `c` to cancel it; verify the fake's request log is empty before `Enter` and holds one `GET /images/get` after it, and that a cancelled job keeps no index
- [x] 4.3 Draw the tree: name, size, type and layer columns, indentation, `Right`/`Enter` to expand and `Left` to collapse or go to the parent, `s` ordering by name or size, and left-truncation that keeps the basename; verify a behaviour test expands a directory, orders by size, and asserts the drawn rows at 80 columns
- [x] 4.4 Wire `/` to the filter with ancestors, the match count and the may-miss line; verify a behaviour test filters to a deep path, sees its three ancestors, and clears with `Esc` back onto the same path
- [x] 4.5 Draw the detail block: full path, type, mode, uid and gid, mtime, size with its source, layer and instruction, and for a container the change kind; verify a test on a file from layer 7 shows `7 of 12` and its instruction, and a changed file's size says it is the image's
- [x] 4.6 Draw a container's overlay states and the un-indexed state, show the age of the change list, and make `r` re-read only the changes; verify behaviour tests for the four states, for opening before the image is read, and that `r` sends no archive request
- [x] 4.7 Add the view's keys to the help overlay; verify `?` lists `F`, the movement keys, `s`, `/`, `w`, `r`, `c` and `Esc`, and the footer of the focused view shows them
- [x] 4.8 Write the README section for the view — the two sources, the bounds, the ages, and the refusal sentences — and update "What it does not do" with "no container archive, no file contents, no file-level difference"; verify the section names the 200,000-entry bound and the zstd refusal

## 5. Export, sandbox and the end-to-end proof

- [x] 5.1 Add the Files source to the evidence dialog: text and JSON, each path with type, size and layer, names escaped, refusing with a reason when nothing is indexed; verify export tests for both formats, for the refusal, and for a hostile name that stays escaped in the file
- [x] 5.2 Seed the sandbox: an image with a deep tree, a whiteout, an opaque directory, a symlink and a hostile name; a container with the four change cases; verify `make sandbox` shows both the complete tree and each overlay state
- [x] 5.3 Add an end-to-end session — open `python:3.12-slim` › Files, cancel once, read it, filter, export, then open a container's Files and read its overlay — and capture the screens at 80×24 and 120×30; verify `make e2e` regenerates them under `docs/screens`
- [x] 5.4 Add the round to `docs/TESTING.md`: what is proven (the merge, the bounds, the escaping, the overlay, the request log), the measurements (index time and memory at the cap), and what is not proven (a container's own archive is never read, and no real daemon was touched); verify the section says the archive was never called
- [x] 5.5 Update "Trying it safely" in the README to name what the sandbox now holds; verify the paragraph mentions the hostile name and the four change cases

## 6. Integration

- [x] 6.1 Run `make test`, `make race` and `make vet` and confirm all pass
- [x] 6.2 Run `make crosscheck` and confirm the real `docker` CLI's `docker diff` agrees with the fake's change list for the seeded container
- [x] 6.3 Against the real daemon, read-only: open Files on one real image, read its archive, and open it on one real container, comparing the changes with `docker diff` by hand; record the outcome in the change and confirm the before/after snapshot of containers, images, volumes and networks is unchanged
- [x] 6.4 Confirm the view cannot read contents or send anything but `GET`: run `make real-readonly-check`, and grep that `internal/files` opens no file and that the only new engine call is `ContainerChanges` over a `GET`

## Workflow follow-up

- Archive the change with `openspec archive files-explorer` once the review requirements are satisfied; archiving updates the main spec.
