# Design

## Context

See `proposal.md` — Why. Three things in the current code shape the approach.

The text the desk shows lives in two forms. The Details tabs that use `textView`
(`details.go:54`) hold source lines that are not laid out for a width, plus a lazy wrap for the
screen. Logs and Events do not use `textView` at all: they read a bounded ring
(`live/hub.go`, 5,000 lines and 1,000 entries). Four builders are functions of the terminal's
width — `changesLines(t, w)`, `declaredLines(t, w)`, `processTable(list, w)` and `eventText(e, w)`
— and `fit` (`format.go:13`) pads every line to exactly `w` and cuts it with `…` when it does not
fit.

The file side already has one writer: `report.WriteFile` (`report/file.go:68`) creates `0600`,
refuses an existing path and a missing folder, and writes through a hidden temporary file. The
dialog that gathers a path, shows the absolute path, size and mode, and arms a fresh `y` is
`exportDialog` (`ui/export.go`). `report.DefaultPath` and `report.Slug` build the prefilled name.

The desk runs in the alternate screen (`cmd/docker-desk/main.go:148`), so nothing it drew survives
the process. `--read-only` must still allow a capture: the spec requires that no request is sent.

## Goals / Non-Goals

**Goals:**

- One rule for what a capture holds, stated once, that a reviewer can check against each pane.
- Byte-identical output for the same held text, from any terminal width.
- No new evidence document, no change to `report.Document` or to `Render`.
- The class distinction visible at the moment of consent, not only in the documentation.

**Non-Goals:**

- Automatic capture, a crash log, or any write the operator did not ask for.
- A complete log: a capture holds what the buffer holds, and sends no request to get more.
- Redaction. Masking environment values in an inspect document would break the reason to capture
  it; the confirmation says what the file is instead.
- Capturing the drawn frame, the Stats chart, or the List table.

## Decisions

### D1 — Capture the held text; rebuild only what the width shaped

Each pane hands the capture its own source, not the screen:

| Pane | Source | Why |
| --- | --- | --- |
| Info | `m.info1.lines` | held, width-free |
| Inspect | `detail.<kind>.Raw` | the daemon's document (D5) |
| Logs | the ring | held, width-free |
| Events | the ring's entries, rendered at the capture width | rows are fitted to `w` |
| Processes | `p.list`, through `processTable` again | rows were laid out at `w` |
| Changes | `changesLines(t, …)` again | prose wrapped at `w` |
| Declared | `declaredLines(t, …)` again | prose wrapped at `w` |
| Job output | `jobOutput(j, m.st)`, the dialog builder | held, width-free |
| File reader | `rd.view.lines` | held, already inert |

The alternative — capture `m.frame` — is a screenshot: guarded, styled, wrapped, clipped and
bound to the last drawn width. It is exactly what the spec rules out.

The lines are collected when `W` is pressed, the same way `exportFindings` collects its document,
so a highlight change behind the dialog cannot alter what is written.

### D2 — Two widths: one that does not clip, one that wraps prose

`fit` pads to `w`; `processTable` gives the last column `w - used`; `eventText` ends in `fit`.
So a table or event builder is run at a large capture width (4,096 cells) and every line is
trimmed on the right afterwards. Prose builders (`changesLines`, `declaredLines`) are run at a
page width (200 cells), which is a readable line in an editor and still leaves values whole.

| Considered | Why not |
| --- | --- |
| One width for everything | 200 cuts a `ps` command; 4,096 makes a diff one line per paragraph |
| The terminal's width | two terminals produce two different files, which the spec forbids |
| No width, builder unchanged | the builders need a number, and `fit` cuts at it |

### D3 — Plain text: strip colour, trim padding

`search.Strip` removes escape sequences (`search/search.go:58`), then `strings.TrimRight(line, " ")`
removes the padding `fit` added. Colour must go: a `.txt` file that paints a terminal is not text,
and the screen's colour is the desk's, not the daemon's.

### D4 — A small pure package for the page rules

`internal/capture` holds the class: a header built from plain facts, lines with colour and padding
removed, the JSON indentation, and the bytes. It imports only the standard library, so nothing in
it can reach the daemon, the UI or a file. The UI passes it strings; the UI calls
`report.WriteFile`. This mirrors the `report` / `report/collect` split (X1) without putting a
capture inside the report package, whose contract is evidence (X5).

| Considered | Why not |
| --- | --- |
| A fourth `report.Document` kind | `report`'s doc promises computed, deterministic, time-free evidence; a capture is none of those |
| Builders inside `ui` only | the rule would be spread over nine call sites with no pure test |
| A new writer | X10: the desk has one place that writes a file, and a second would drift |

### D5 — Inspect is the document, not the rendering

`json.Indent` on `detail.Raw`, written as `.json`, with no header — JSON has no comments. The
screen's `jsonText` escaping exists to protect the terminal; a file needs no such protection, and
the raw bytes are what a reader can parse and diff. If `json.Indent` fails, nothing is written and
the status line says the daemon's document is not JSON: an inspect document that does not parse is
not worth saving under a `.json` name.

### D6 — The dialog grows one field, not a second dialog

`exportDialog` gains the collected capture (lines or raw JSON, and the `holds` sentence) and a
single-entry format list; `Tab` and the arrows do nothing when there is one format. The path,
size, mode, `y`, arming and writer stay the same, which is what keeps the file rules true by
construction rather than by a second implementation.

The `holds` sentence is built per pane, for example:

- Inspect: `holds: the daemon's inspect document (environment values included)`
- Logs: `holds: 4,812 held log lines as the desk shows them, colour removed`
- Changes: `holds: the re-creation diff as the desk built it`

### D7 — `W`, and where it is routed

`W` is unbound (verified against `keys.go` and `actions.go`), reads as the larger sibling of `w`,
and needs no modifier that a terminal may swallow. It is handled where each pane already handles
its keys: the Details key switch (before the `w` case), `onEventsKey`, `onJobsKey`, and
`onReaderKey`. The List and Stats do nothing on `W`; Findings, Packages and Upstream keep `w`.

`ctrl+s` was considered and rejected: many terminals use it for flow control, and the desk's keys
are otherwise unmodified letters.

### D8 — Logs capture the ring, filtered if a filter is active

The capture reads the whole ring through `ring.Read(first, end)`, oldest first, including the
stream's marker lines. With a filter, it keeps the lines `logMatches` names — the same match
function the drawing uses — and the header states the query and `matched of held`. If the stream's
target is not the highlighted container (the pane is showing "opening log stream…"), nothing is
held and the capture refuses. The header states that the buffer is bounded, so an excerpt never
looks like the whole log.

### D9 — The decision record gets its own group

The export decisions are `X1–X16` and describe evidence. Capture decisions become `Y1–Y6`, so a
reader of X5 does not conclude that a captured inspect document is bound by it.

## Risks / Trade-offs

- **A capture can be large**: 5,000 lines of up to 2,048 bytes is about 10 MB in memory and on
  disk. The confirmation shows the exact size, and the operator may choose another pane or cancel.
  No cap is added; a cap would be a second, invisible rule.
- **Secrets on disk**: an inspect document carries environment values, and a log may quote them.
  Mitigated by the confirmation sentence, mode `0600`, and the fact that the operator asked for
  this file. It is a class the documentation states plainly, not a promise of redaction.
- **A rebuild can drift from the screen**: `changesLines` and `declaredLines` are called with a
  different width, and a future change to either builder could make the capture disagree with the
  view. Mitigated by one test per pane that captures at two terminal widths and compares bytes,
  and by keeping the mapping from pane to builder in one function.
- **The page width is a judgement**: 200 is chosen for reading. A user with wider editors may want
  more; the number is one constant, and the file states that it was laid out for a page.
- **`W` competes with `w` in the help**: two keys that differ only by case and both concern text.
  Mitigated by adjacent help lines and distinct footers (`w wraps`, `W saves this text`).
