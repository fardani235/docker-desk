# Spec Delta

## Purpose

Lets the operator keep the text a pane is showing — the daemon's inspect document, a log window,
the events of a session, a process snapshot, a re-creation diff, a job's output — as one file on
this machine, before the desk or the highlight takes it away.

## ADDED Requirements

### Requirement: A key that captures the focused pane's text
`W` SHALL open the evidence dialog on the text of the focused pane, and the file SHALL be written
only after that dialog's confirmation. `W` SHALL be offered in Info, Inspect, Logs, Processes,
Changes and Declared, in the Events pane, in the Jobs pane for the selected job's output, and in
the file reader. It SHALL not be offered on Stats, Findings, Packages, Upstream or the List.

#### Scenario: A Details tab
- **WHEN** the Details pane is focused on Logs and `W` is pressed
- **THEN** a dialog opens with a path and one line saying what the file will hold, and nothing is written until it is confirmed

#### Scenario: The Events pane
- **WHEN** the Events pane has focus and `W` is pressed
- **THEN** the dialog offers the events the pane holds

#### Scenario: The Jobs pane
- **WHEN** a finished job is selected in the Jobs pane and `W` is pressed
- **THEN** the dialog offers that job's output

#### Scenario: A pane with no capture
- **WHEN** the List pane or the Stats tab has focus and `W` is pressed
- **THEN** no dialog opens, nothing is written, and no other action runs

### Requirement: Laid out for a page, not for the screen
The captured text SHALL be built at a width that leaves every value whole and SHALL wrap prose at
a fixed page width, SHALL have colour sequences removed and SHALL have trailing padding trimmed.
The same held text SHALL produce the same bytes whatever the terminal's width.

#### Scenario: Two terminal widths
- **WHEN** the same log is captured from an 80-column terminal and from a 200-column terminal
- **THEN** the two files hold identical bytes

#### Scenario: A value is not cut
- **WHEN** a process command is wider than the terminal and the Processes view is captured
- **THEN** the command appears in full in the file

#### Scenario: Prose is wrapped at the page
- **WHEN** the Changes view is captured and its prose is wider than the page
- **THEN** it is wrapped at the page width, not at the terminal's width

#### Scenario: No colour
- **WHEN** any pane is captured with colour on
- **THEN** the file holds no escape sequence

### Requirement: A text capture says what it holds
A text capture SHALL begin with `#` lines naming the tool, the pane and the object, SHALL state
an active filter with its query and its count, and SHALL state that the text was laid out for a
page. An Inspect capture SHALL have no header.

#### Scenario: Filtered logs
- **WHEN** the Logs view is filtered and captured
- **THEN** the header states the query and how many of the held lines match, and the file holds only those lines

#### Scenario: Unfiltered logs
- **WHEN** the Logs view has no filter and is captured
- **THEN** the header states the number of lines and that every held line is present

### Requirement: Inspect leaves as the daemon's document
On Inspect, `W` SHALL write the daemon's inspect document, indented, as JSON, with no header, and
the prefilled name SHALL end in `.json`. The file SHALL parse as JSON.

#### Scenario: The captured document parses
- **WHEN** the Inspect tab is captured and the file is read back
- **THEN** it parses as JSON and describes the same object as the tab

#### Scenario: No header in JSON
- **WHEN** the Inspect capture is opened
- **THEN** its first line is the document's opening brace, not a comment

### Requirement: A capture is not a report
A capture SHALL hold the text the desk drew, with colour removed, and SHALL keep the daemon's
values as they are — times, environment variable values and control characters included. This
SHALL NOT change what a report may carry. The confirmation SHALL name the class in one line.

#### Scenario: The Inspect confirmation
- **WHEN** an Inspect capture is confirmed
- **THEN** the dialog says the file is the daemon's inspect document and includes environment variable values

#### Scenario: A log confirmation
- **WHEN** a Logs capture is confirmed
- **THEN** the dialog says the file is the text as the desk holds it, not a report

### Requirement: Logs and Events capture the buffer, not the window
A Logs capture SHALL hold every line the buffer holds, from oldest to newest, with the stream's
markers, and not only the rows in view. An Events capture SHALL hold every event the pane holds,
not only the rows in view.

#### Scenario: Scrolled back
- **WHEN** the operator has scrolled back in Logs and captures
- **THEN** the file holds the newest held lines too

#### Scenario: More events than rows
- **WHEN** the Events pane holds more events than its pane can show and is captured
- **THEN** every held event is in the file

### Requirement: The other panes capture what they hold
Info, Processes, Changes, Declared, the file reader and a job SHALL each capture the text they
currently hold: the summary lines, the snapshot that was taken, the comparison that was built, the
preview's text, and the job's output so far.

#### Scenario: A process snapshot
- **WHEN** the Processes view shows a snapshot and is captured
- **THEN** the file holds that snapshot, not a new listing

#### Scenario: A re-creation diff
- **WHEN** the Changes view has finished its comparison and is captured
- **THEN** the file holds the comparison as the pane built it

### Requirement: Nothing held, nothing written
When the focused pane holds no text yet — an inspect document still loading, a log stream not
open, a comparison not built, a job with no output — `W` SHALL write nothing and the status line
SHALL say why in one sentence.

#### Scenario: Inspect loading
- **WHEN** `W` is pressed while the Inspect tab says it is loading
- **THEN** nothing is written and the status line says the document has not arrived

#### Scenario: No log stream
- **WHEN** `W` is pressed on Logs before any line has arrived
- **THEN** nothing is written and the status line says there is nothing held

### Requirement: The file rules are the desk's one set
A capture SHALL send no request to the daemon, SHALL write only the one path the dialog showed,
SHALL create the file with mode `0600`, SHALL never replace an existing file, SHALL refuse a
missing folder without creating it, and SHALL work in read-only mode.

#### Scenario: Read-only
- **WHEN** the desk runs with `--read-only` and a capture is written
- **THEN** the file is written and the daemon received no request because of it

#### Scenario: A path already taken
- **WHEN** the confirmed path already holds a file
- **THEN** nothing is replaced and the dialog says a file is already there

#### Scenario: A missing folder
- **WHEN** the confirmed path is inside a folder that does not exist
- **THEN** nothing is written, no folder is created, and the status line says so

### Requirement: The prefilled name
The path SHALL be prefilled as `~/docker-desk-<kind>-<object>-<YYYYMMDD>.<ext>`, where the kind is
`info`, `inspect`, `logs`, `processes`, `changes`, `declared`, `events`, `output` or `file`, and
the object is the highlighted object's name, or for the Events pane the focused daemon's context
name (`engine` when there is none).

#### Scenario: A container's logs
- **WHEN** the Logs view of `shop-web-1` is opened for capture
- **THEN** the path is prefilled on `~/docker-desk-logs-shop-web-1-<date>.txt`

#### Scenario: The Events pane
- **WHEN** the Events pane is captured on a daemon whose context is `prod`
- **THEN** the prefilled name carries `events-prod`

### Requirement: The key is listed
`W` SHALL be listed in the help dialog and the README, and the Details footer SHALL list it
where the line has room.

#### Scenario: The help dialog
- **WHEN** the help dialog is opened
- **THEN** it lists `W` and what it saves, next to `w` and what that does

#### Scenario: The footer
- **WHEN** the Details pane is wide enough for every hint
- **THEN** its footer names `W` and what it saves
