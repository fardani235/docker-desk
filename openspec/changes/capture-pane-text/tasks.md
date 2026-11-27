# Tasks

## 1. The page rules (`internal/capture`)

- [ ] 1.1 Create `internal/capture` with `Text`: `#` header lines, colour sequences removed, trailing spaces trimmed, one trailing newline. Verify: `go test ./internal/capture` passes a table whose lines carry SGR sequences and `fit` padding, and the output holds neither.
- [ ] 1.2 Add `JSON`: indent a document with `json.Indent`, no header, one trailing newline, and an error when the bytes are not JSON. Verify: the unit test parses the output with `json.Valid` and asserts the first byte is `{`; an invalid input returns an error and no bytes.
- [ ] 1.3 Keep the package free of the desk: it imports the standard library only. Verify: `go list -deps ./internal/capture` shows no `internal/` package, and the package doc states the class.

## 2. The key, the dialog and the writer

- [ ] 2.1 Add a collected capture (kind, object, pane, bytes, `holds` sentence) and `openCapture`, and route `W` in the Details key switch for Info, Inspect, Logs, Processes, Changes and Declared. Verify: a UI test pressing `W` on Logs opens the dialog, writes nothing before `y`, and writes exactly one file after `y`.
- [ ] 2.2 Teach `exportDialog` a single-format capture: one format line, `Tab` and the arrows do nothing, and the dialog shows the `holds` sentence. Verify: a UI test asserts the format line and that `Tab` leaves the format unchanged.
- [ ] 2.3 Route the result through `report.WriteFile` and the existing status wording: `Saved <format> to <path> (N bytes, mode 0600).`, and `Not saved: …` on a refusal. Verify: a UI test asserts the status line, and that a path already holding a file is refused with nothing replaced.
- [ ] 2.4 Make the refusal sentences for an empty pane: inspect not yet arrived, no log stream, comparison not built, job with no output. Verify: one UI test per case asserts nothing is written and the status line says why.
- [ ] 2.5 Prove read-only: with `--read-only`, a capture is written and the fake daemon receives no request because of it. Verify: a UI test compares the request count before and after the capture.

## 3. Each pane's source

- [ ] 3.1 Info: capture `m.info1.lines`. Verify: a UI test captures a container's Info at 80 and at 200 columns and asserts identical bytes.
- [ ] 3.2 Inspect: capture `detail.<kind>.Raw` as JSON, name it `.json`, no header. Verify: a UI test parses the file and asserts it names the highlighted object.
- [ ] 3.3 Logs: capture the whole ring oldest first, including marker lines, filtered through `logMatches` when a filter is active, with the query and `matched of held` in the header; refuse when the stream's target is not the highlighted container. Verify: UI tests for an unfiltered capture (every held line present), a filtered capture (only matches, counts in the header), and a scrolled-back position (the newest lines are still in the file).
- [ ] 3.4 Events: capture the ring's entries through `eventText` at the capture width. Verify: a UI test asserts the file holds more events than the pane can show, and holds no `…` cut.
- [ ] 3.5 Processes: capture the held `p.list` through `processTable` at the capture width, with the snapshot's time in the header. Verify: a UI test with a command longer than the terminal asserts the command appears in full.
- [ ] 3.6 Changes and Declared: rebuild at the page width and capture; refuse when the comparison is not built. Verify: UI tests assert a prose line is wrapped at the page width, not at the terminal's, and that a capture before the comparison writes nothing.
- [ ] 3.7 Job output and the file reader: capture `jobOutput` for the selected job and `rd.view.lines` for the open file. Verify: one UI test per pane asserts the captured text matches what the pane shows.

## 4. Keys, help and documentation

- [ ] 4.1 Add `W` to the help dialog and to the Details footer where the line has room, and update the tests that assert the current hints (`reader_test.go`, `behaviour_test.go`). Verify: `go test ./internal/ui` passes and `make e2e` regenerates `docs/screens`; the changed screens show the new key.
- [ ] 4.2 Add a documentation test in the style of `TestDocumentedDaemonKeysAreTheKeySwitchs`: the README's key table and the help both list `W` and what it saves. Verify: the test fails before the README is edited and passes after.
- [ ] 4.3 Write the README: the key table entry, a Capture subsection beside Export with the class table (capture versus report), the prefilled names, and the fact that a capture holds the daemon's text as it is. Verify: the task 4.2 test passes, and every command and key the section names exists.
- [ ] 4.4 Write `docs/DECISIONS.md` `Y1–Y6`: the held-text rule, the two widths, the class split from X5, the JSON document, the single dialog and the key. Verify: each decision names its alternative and its cost, as the existing record does.

## 5. Integration

- [ ] 5.1 An end-to-end test against the sandbox: open a container's Logs and Inspect, capture both, assert the files' bytes, assert the inspect file parses, and assert no request was sent by either capture. Verify: `go test ./internal/e2e -run Capture -count=1` passes.
- [ ] 5.2 Run the whole suite and the static checks. Verify: `make test`, `make vet` and `make e2e` pass, and `openspec validate capture-pane-text --strict` is valid.

## Workflow follow-up

- Archive the change after review.
