# Proposal

## Why

A value in the Details pane is reachable only by scrolling. A container's inspect document is
hundreds of lines; its log holds up to 5000. At 80×24 the pane shows seven rows.

## What Changes

- `/` in the Details pane, on any tab, opens a search bar in the pane's last content row. It
  costs no row while closed; on Logs it lives in the status row.
- Info, Inspect, Processes and Changes **find**: nothing is hidden, matches are highlighted as
  the query is typed, the bar counts them (`find: q  3 of 9`), Enter jumps to the current one,
  `n` and `N` step through them, wrapping. On Inspect the bar names the current match's key
  path (`at Config.Health.Status`).
- Logs **filters**: lines that do not match are hidden as the query is typed; following shows
  the newest matching line, scrolling back moves through matching lines, and the status row
  counts them against the lines held — never claiming the daemon's older output was searched.
- Esc clears the query; a second Esc returns to the List.

## Capabilities

### New Capabilities

- `details-search`: find in the text tabs, filter in Logs, the key path, keys and limits.

### Modified Capabilities

- None. `n` is shadowed only while a search is open; every other key keeps its meaning.

## Impact

- `internal/search`: new, pure — case-insensitive matching on ANSI-stripped text and the
  marking of match ranges inside styled text.
- `internal/keypath`: new, pure — the key path of a line of indented JSON.
- `internal/ui`: `textView` gains a find (memoised matches, a pending seek resolved in
  `render`); `search.go` holds the bar, keys and the Logs filter; hints, help, styles.
- No engine request, nothing sent, nothing written; read-only mode is unaffected.
