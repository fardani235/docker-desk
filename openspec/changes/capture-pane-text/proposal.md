# Proposal

## Why

The desk holds a great deal of text that exists nowhere else: the daemon's inspect document, the
lines a log stream has produced, the events of a session, a process snapshot, a re-creation diff,
the output of a job. The desk runs in the alternate screen, so all of it is discarded when the
desk closes, and some of it is discarded earlier still — the log buffer is dropped when the
highlight moves to another container. Today `w` can write only four computed documents
(findings, packages, space, files); none of them is the text on screen. An operator who wants to
keep an inspect document, or the last thousand log lines that explain a failure, has no way to do
it.

## What Changes

- `W` SHALL write what the focused pane holds to one local file, through the existing evidence
  dialog and the one file writer. It SHALL be offered in Info, Inspect, Logs, Processes, Changes,
  Declared, the Events pane, the Jobs pane (the selected job's output) and the file reader. It
  SHALL NOT be offered on Stats, Findings, Packages or Upstream.
- The file SHALL be laid out as a document, not a screenshot: no value is cut, prose is wrapped
  at a fixed page width, colour sequences are removed and trailing padding is trimmed. So the same
  text captured from an 80-column terminal and from a 200-column terminal SHALL be identical.
- A text capture SHALL carry `#` header lines naming the tool, the object, the pane, any active
  filter with its counts, and the fact that it was laid out for a page. An Inspect capture SHALL
  instead be the daemon's inspect document, indented, as parseable JSON, with no header.
- A capture SHALL be a new file class: it keeps the daemon's text as it is, including times and
  environment variable values, and the confirmation SHALL say so in one line. The rules that make
  a report reproducible and free of times, host paths, daemon addresses and environment values
  apply to reports (findings, packages, space, files) and SHALL NOT apply to captures.
- A capture SHALL send no request to the daemon, SHALL write only the one path shown in its
  confirmation, SHALL create the file with mode `0600`, SHALL never replace an existing file, and
  SHALL work in read-only mode.
- The key list, the pane footers, the help dialog, the README and the decision record SHALL state
  the new key, the new class and the difference between the two.

## Capabilities

### New Capabilities

- `text-capture`: what a pane's text is when it leaves the desk — which panes offer a capture,
  how the lines are laid out for a page rather than a screen, the `#` header and the JSON
  document, the class rule that a capture keeps the daemon's text while a report does not, the
  name and the confirmation, and the guarantee that a capture sends nothing.

### Modified Capabilities

None. `files-explorer` keeps its own `w` export of the file listing; `image-archive-save` keeps
`w` for the archive; `mutation-safety` already states that the desk writes exactly the one local
path its dialog named, which a capture follows. No existing requirement changes.

## Impact

- `internal/ui`: the capture key and its routing in `keys.go`, the capture builders in
  `details.go`, `panes.go`, `processes.go`, `changes.go`, `declared.go`, `reader.go` and the job
  dialog, the single-format case in the export dialog (`export.go`), and the footer and help text
  in `view.go`.
- `internal/report`: no new evidence document. A capture reuses `report.WriteFile`,
  `report.ExpandPath`, `report.DefaultPath` and `report.Slug`; the serializers and
  `report.Document` stay about reports.
- A new small package for the page rules (stripping, trimming, the header, the JSON indentation),
  so the rule is in one place and can be tested without a terminal.
- Tests: one per pane builder at two terminal widths, asserting identical bytes; the class line in
  the confirmation; a capture in read-only mode sending no request; the refusals (nothing held
  yet, an existing path, a missing folder).
- Documentation: the README's key list and Export section gain the class distinction, and
  `docs/DECISIONS.md` gains the reason a capture is not a report.
