# Tasks

## 1. Pointer plumbing and lifecycle

- [ ] 1.1 Add `NoMouse` to `ui.Options` (zero value = enabled) and a `--no-mouse` flag in `cmd/docker-desk`, passing it through to the model. Verify `go test ./cmd/docker-desk` passes and a new flag test shows `--no-mouse` parsed and `--help` naming it.
- [ ] 1.2 Append `tea.WithMouseCellMotion()` to the program options only when mouse is enabled. Verify with a focused test of the option builder (or a pty run showing `\x1b[?1002h` and `\x1b[?1006h` on startup) and none under `--no-mouse`.
- [ ] 1.3 Re-enable mouse reporting after a shell hand-off: when `shellDoneMsg` is handled and mouse is enabled, return a command yielding `tea.EnableMouseCellMotion()`; under `--no-mouse` return nothing extra. Verify with a model test that the `Update` command produces the enable message, and produces none with `NoMouse`.
- [ ] 1.4 Add the pointer section to the help overlay (click focuses/selects, wheel scrolls, `--no-mouse`) and describe the flag in `--help`. Verify the help test shows the pointer lines and the flag test shows the description.

## 2. Frame-captured hit map

- [ ] 2.1 Add the `hit` type, the ordered hit list, `record` and reverse lookup, built into a local slice and assigned at the end of a fresh render; clear it on quit and on `WindowSizeMsg`. Verify a test renders 140×40 and finds the pane hit at the centre of each of the five panes, and that a cached `View` keeps the same map.
- [ ] 2.2 Record the desk regions while drawing: pane rectangles, Kinds rows, List rows (index and key), Details tab spans, Events rows, Jobs rows, and daemon-strip entries. Verify hit tests for one coordinate per region at wide and compact layouts, including an entry that is scrolled out of view (no hit).
- [ ] 2.3 Clear and rebuild the map across redraws: after changing kind, filter, scroll offset, focus or terminal size, verify a click at the same cell resolves to the new row and never the old one; verify a resize before repaint leaves no hits.

## 3. Clicks on the desk

- [ ] 3.1 Implement left-press dispatch for pane focus, kind selection, list highlight, Details tab, daemon strip and job selection, reusing `selectIndex`, `setKind`, `focusMember`, `jobSel` and direct tab assignment. Verify model tests for each spec scenario, including that a row click opens no menu and does not change the selection marks.
- [ ] 3.2 Implement Events row clicks by holding the ring and placing the cursor on the clicked entry (and no-oping when the pane is empty). Verify a test that clicks an older event and sees the pane held with the cursor on that row.
- [ ] 3.3 Focus text rows on click — list filter, Details search, form field rows, Repository name, export path — with the caret at the end; clicking elsewhere ends the edit, keeps its text and does not submit a form or ask the registry. Verify tests for the filter, a form field and the repository name.
- [ ] 3.4 Add the mouse gate: ignore events under `NoMouse`, when too small, with no daemon, before any frame, for motion/release and for buttons other than left/wheel. Verify a table test of each ignored case leaves focus, kind, selection and dialogs unchanged.
- [ ] 3.5 Record dialog rows while drawing and handle their clicks: a menu entry invokes as Enter, a picker/daemons/job-viewer row moves its selection, and a second click on a selected picker row chooses it; help and job text ignore clicks. Verify tests for the menu invocation, picker select-then-choose and an inert help click.

## 4. Wheel

- [ ] 4.1 Factor the movement primitives the keys use into shared movers and implement the wheel: three rows in List, Details, Events and Jobs; one kind in Kinds; no focus change. Verify tests for List, Kinds, Jobs, Details (including logs leaving follow) and Events (hold on upward, resume on downward at the newest).
- [ ] 4.2 Implement wheel movement in dialogs: a confirmation's object list, help and job text. Verify a test that wheels a confirmation and sees its list scroll with no request sent, and that clicks still do nothing.

## 5. Overlays

- [ ] 5.1 Record Files rows while drawing; click sets the files cursor and wheel moves it three rows, with no archive read started. Verify tests for a row click, a wheel notch and an unchanged request log.
- [ ] 5.2 Record Space rows while drawing; click sets the space cursor and wheel moves it three rows, with no measurement started. Verify tests for a row click, a wheel notch and an unchanged request log.
- [ ] 5.3 Record Repository rows while drawing; click sets the repo cursor and wheel moves it three rows, with no registry or manifest request. Verify tests for a row click, a wheel notch and an unchanged request log.
- [ ] 5.4 Wire the Files filter and Repository name line into the shared text-row handling; clicking one focuses it with the caret at the end and clicking away keeps its text without starting a read or an ask. Verify tests for both.

## 6. Safety, end to end, and documentation

- [ ] 6.1 Prove the pointer never mutates with the fake engine as witness: click every cell of a stop confirmation and a typed-word confirmation, and click a menu's `Remove` entry; assert no request was sent, no job started, and the menu click only opened the same plan the key opens. Verify `go test ./internal/ui -run Mouse`
- [ ] 6.2 Add pty tests that send SGR mouse sequences: click a container row (highlight and Details follow), wheel over Details (three lines), and run with `--no-mouse` (no `\x1b[?1002h`/`?1006h` written and a click changes nothing). Verify `go test ./internal/e2e -run Mouse`.
- [ ] 6.3 Add a pty test that leaves a container shell and then clicks a row, proving reporting was restored; verify the click changes the highlight after the shell ends.
- [ ] 6.4 Update README: the opening line, the flag table with `--no-mouse`, and a short Mouse section covering click, wheel, terminal-selection override and the read-only/`--no-mouse` behavior. Regenerate affected screen captures (help overlay) with the project's `-update-screens` flow and verify `git diff` shows only the intended captures.
- [ ] 6.5 Record the decision in `docs/DECISIONS.md` (frame-captured hit map, keyboard-only consent, `--no-mouse`) and verify the file's numbering and cross-references are consistent.
- [ ] 6.6 Run the full suite with and without the race detector (`go test ./...`, `go test -race ./...`) and verify every existing keyboard test still passes and the new pointer tests are green.

## Workflow follow-up

- Archive the change once review is complete.
- Verify the archived specs and the change directory as the project expects.
