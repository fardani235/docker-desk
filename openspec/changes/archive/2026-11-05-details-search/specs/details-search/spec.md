## Purpose

Defines search in the Details pane: find in the text tabs, filter in Logs, the Inspect key
path, the keys, and what the search does not claim.

## ADDED Requirements

### Requirement: One bar, opened with `/`, that costs no row while closed
With the Details pane focused, `/` SHALL open a search bar on any tab, in the pane's last
content row (on Logs, the status row). While no query is set and the bar is not being typed
in, the pane SHALL draw exactly as before. Each tab SHALL keep its own query; every query
SHALL be cleared when the highlighted object changes.

#### Scenario: Typed keys never act
- **WHEN** the bar is being typed in and any printable key is pressed — including an action
  key, `q`, `?`, `S`, `T`, `r`, a digit, `[` or `]`
- **THEN** it becomes part of the query; no dialog opens, no tab, pane or theme changes

### Requirement: Find in Info, Inspect, Processes and Changes
Matching SHALL be a case-insensitive substring test on the text with escape sequences removed,
whether or not colour is on. Non-matching lines SHALL stay on screen. Matches SHALL be
highlighted while the query is typed, and the bar SHALL read `find: <q>  <i> of <n>`. Enter
SHALL close the input, keep the query and bring the current match into view; `n` and `N` SHALL
move to the next and previous match, wrapping at either end.

#### Scenario: A match on the second row of a wrapped line
- **WHEN** the current match lies in the part of a long line wrapped onto a later row
- **THEN** the jump brings that row, not the line's first row, into view

### Requirement: The Inspect key path
On Inspect the bar SHALL name the JSON key path of the current match's line, derived from the
indentation and the last `"key":` at each depth, array elements as `[n]`.

#### Scenario: A nested key
- **WHEN** the current match is on `"Status": "healthy"` inside `Config.Health`
- **THEN** the bar reads `at Config.Health.Status`

### Requirement: Filter in Logs
Lines that do not match SHALL be hidden as the query is typed. Following SHALL show the newest
matching line; scrolling back SHALL move through matching lines only. The status row SHALL read
`● following · <m> of <held> match`, and when nothing matches,
`no match in the <held> lines held`. It SHALL NOT claim that output older than the lines held
was searched.

#### Scenario: New output while filtered
- **WHEN** a matching line arrives while following with a query
- **THEN** it is shown at the bottom and the count rises; a non-matching line changes nothing
  on screen but the held count

### Requirement: Esc one step at a time
Esc SHALL clear the current tab's query and close the bar; Esc with no query SHALL return to the
List.

#### Scenario: The ladder
- **WHEN** a query is kept and Esc is pressed twice
- **THEN** the first clears it and closes the bar, the second focuses the List

### Requirement: `n` and `N` shadow actions only while a search is open
While the current tab has a query, `n` and `N` SHALL step through matches; otherwise `n` SHALL
keep its action (new volume, new network).

#### Scenario: Wrapping at the end
- **WHEN** the current match is the last and `n` is pressed
- **THEN** the first match becomes current and is brought into view

### Requirement: Nothing is sent
Searching SHALL send no request to the daemon and SHALL write nothing, in any mode.

#### Scenario: Read-only
- **WHEN** the desk runs with `--read-only` and a search is typed, kept, stepped and cleared
- **THEN** the behaviour is identical and the daemon receives no request because of it

### Requirement: A highlight that does not depend on colour
A match SHALL be bold and underlined (in the accent colour when there is colour), the current
one also reversed. Without colour, where no attribute is drawn, each match SHALL be bracketed.

#### Scenario: Every theme
- **WHEN** any built-in theme is in use
- **THEN** the match style is bold and underlined, and its colour passes the contrast check
