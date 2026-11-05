## Context

Info, Inspect, Processes and Changes draw through `textView`, which wraps source lines for the
width it is drawn at; one source line is one or more rows, and the width is known only inside
`render`. Logs is a `live.Ring[string]` read by absolute index, bottom-anchored, with colour
kept from the daemon. Keys reach the Details pane through `onDetailsKey`, which falls through to
the kind's action keys (B7).

## Goals / Non-Goals

Goals: reach a value without scrolling; never mislead about what was searched; no key typed
into the bar can act; constant work per frame while nothing changes.

Non-goals: regular expressions; searching Stats or Findings (not text); fetching older log
lines from the daemon; remembering queries across objects.

## Decisions

Recorded in full in `docs/DECISIONS.md`, round ten (Q1–Q14). In short: find where text is a
document, filter where it is a stream; matching folds case on ANSI-stripped text; a jump is a
pending seek resolved after layout; matches are memoised by (content version, query), the Logs
filter incrementally by (ring, query, scanned-to); `n`/`N` shadow actions only while a search
is open; the highlight is bold and underlined (plus the accent colour), the current match also
reversed, and without colour the matches are bracketed `»…«`.

## Risks / Trade-offs

- The Logs filter sees only the lines held (at most 5000, the last 1000 on opening); the status
  row says how many, and that older ones scrolled out.
- A query that changes the matched set renumbers `3 of 9`; the current match is re-chosen from
  the top of the view, not remembered by content.
