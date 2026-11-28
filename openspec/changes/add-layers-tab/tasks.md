# Tasks

## 1. The report keeps every step (`internal/forensics`)

- [ ] 1.1 Add `Step{Instruction, Empty, Layer}` and `Report.Steps`, and fill it where `layerOfHistory` is built in `rules.go`, from the same config history array the instructions come from. Verify: a unit test with a 5-entry history (3 layers, 2 empty) asserts order, the `Empty` flags and the layer numbers; a history whose count does not bind gives `Layer` 0 for every step.
- [ ] 1.2 Keep `Instructions` and `Steps` one fact. Verify: a unit test asserts that for every step with `Empty` false, `Report.Instruction(step.Layer)` equals the step's instruction.
- [ ] 1.3 Do not change the exported image report. Verify: `go test ./internal/report/...` passes with no golden fixture edited, and a new step list is absent from the JSON document.

## 2. Sharing by content (`internal/space`)

- [ ] 2.1 Add `Diffs []string` to `space.Image` and a diff-ID holders map to `Index`, with `ContentHolders(diffID)` and `ContentSharers(id)`; exclude the image itself and count a repeated diff ID once per holder. Verify: a unit test with two images sharing a diff ID at different depths asserts each names the other; an image holding the same diff ID three times is one holder; holders are sorted.
- [ ] 2.2 Leave the byte accounting alone. Verify: `go test ./internal/space` passes with no existing assertion changed, and `Agrees`/`Frees` return the same answers for the existing fixtures.

## 3. One layer cache for the desk (`internal/ui`)

- [ ] 3.1 Move `layerInfo` and the per-image map out of `spaceState` into a desk-level cache, and point the Space job at it. Verify: `go test ./internal/ui -run Space` passes, and a test opens the Space view twice and counts no `ImageLayers` call the second time.
- [ ] 3.2 Add the read job: fetch only the images the cache lacks, with one progress line (`layers of image 12 of 412`). Verify: a test with three images and one already cached asserts two `ImageLayers` calls and the progress wording.

## 4. The tab (`internal/ui/layers.go`)

- [ ] 4.1 State, target and sync: the highlighted image, its platform and its cache key; read its own layers once; start the Findings analysis through the existing job and consent gate; cancel the analysis when the highlight moves; do nothing while the tab is not shown. Verify: a UI test for a first open (one history call, one analysis job), a second open on an analysed image (no archive request), and a move to another image (the first analysis cancelled).
- [ ] 4.2 The list before the archive: build rows from the daemon's history, bind an entry to a layer only when the count of entries with a size equals the layer count, mark the rest config only, and say the binding was counted. Verify: a test with 18 entries and 7 layers shows 11 config-only rows and 7 bound; a test with a mismatch binds no row and says why.
- [ ] 4.3 The list after the archive: prefer `Report.Steps`, keep the cursor on the same index, and change the source line to the archive's history. Verify: a test asserts the exact list replaces the counted one, the cursor stays on the same index, and the header names the new source.
- [ ] 4.4 Rows and the highlighted-step block, at 120, 80 and 60 cells: the instruction, the layer, the size, the holders count, with the columns dropped in that order; the block shows the masked instruction, the digest and its `diff_id` match, the file and stored bytes, the files, directories, deleted paths and opaque directories, and the findings on that layer. Verify: render tests at the three widths, and block tests for a layer step, a config-only step and an unread layer.
- [ ] 4.5 The summary line: layers shared, the own bytes, the source of each figure, `at least` with the count of images not read, and the not-read state that names `s`. Verify: tests for a shared base, for nothing shared, for an incomplete read and for the state before `s`.
- [ ] 4.6 The states with nothing to show: no history, no layers, the analysis still running, a failed analysis, an unread layer. Verify: one UI test per state asserts its one line and that no row is invented.
- [ ] 4.7 The holders in a row and in the block: count, names, `+N more` for a long list, short IDs for untagged images. Verify: a test with 40 holders asserts the count, the names the pane holds and the remainder.

## 5. The tab in the desk (`details.go`, `keys.go`, `view.go`)

- [ ] 5.1 Append `Layers` to the Images tabs and dispatch its render; route `up`/`down`, `j`/`k`, `g`, `G`, `PgUp`, `PgDn` and space to its cursor. Verify: a UI test reaches it with `]` from Upstream, moves with `G` and `g`, and asserts the other four tabs keep their positions.
- [ ] 5.2 Add `s`, Enter and `c` to the tab, and list the keys in the help dialog and the Details footer where the line has room. Verify: UI tests for `s` starting the read, Enter giving consent on a remote threshold image, and `c` cancelling; the help and footer list the keys.

## 6. Keeping the build (`capture.go`)

- [ ] 6.1 Add the capture builder: every step with its block at the page width, the whole list even when scrolled, routed from `W` on Layers, prefilled as `~/docker-desk-layers-<object>-<date>.txt`. Verify: a UI test captures at 80 and at 200 columns and asserts identical bytes; a scrolled tab's file holds the first step; the prefilled path carries `layers-`.
- [ ] 6.2 Keep the capture rules one set. Verify: `go test ./internal/ui -run Capture` passes, and a test asserts Layers is in the offered list and no longer among the excluded tabs.

## 7. Documentation and screens

- [ ] 7.1 Write the README: the tab in the Images section, the step list, the two sources, the content rule for sharing and how it differs from Space's byte figure, the keys, the key tables, the `layers` capture kind. Verify: a documentation test in the house style asserts the README names the tab and every key it lists, and fails before the edit.
- [ ] 7.2 Write `docs/DECISIONS.md`'s round-thirty section: steps as rows, the cursor list, the archive's history as the authority, content not chain, `s` not auto, the report's step list, two byte figures, the tab's position, `W`, the shared cache, and what the tab refuses to say. Verify: each entry names its alternative and its cost, and the content-versus-chain entry names the empty-layer evidence.
- [ ] 7.3 Write the `docs/JOURNAL.md` round-thirty entry. Verify: it names what was run, what the real engine showed, and every place the plan met something it did not expect.
- [ ] 7.4 Regenerate the screens with `make e2e` and keep the new ones at 120 and 80 columns. Verify: the new files exist under `docs/screens/` and show the tab, its summary and its block.

## 8. Integration

- [ ] 8.1 Extend the sandbox: an image history with empty and non-empty entries, and two images holding the same layer at different depths. Verify: a fake-engine unit test serves both, and the sandbox starts (`make sandbox`).
- [ ] 8.2 An end-to-end test against the sandbox: open Layers on an image, assert the steps and the config-only rows, press `s`, assert the holders, and assert the daemon received no mutating request. Verify: `go test ./internal/e2e -run Layers -count=1` passes.
- [ ] 8.3 Run the whole suite and the static checks. Verify: `make test`, `make vet`, `make e2e` and `openspec validate add-layers-tab --strict` all pass.

## Workflow follow-up

- Archive the change after the project's review requirements are satisfied.
- Verify the archived result: the main specs hold the new capability and the text-capture update.
