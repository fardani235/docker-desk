# Design

## Context

See `proposal.md` for the motivation and behavior contract. The engine package is the only Docker-daemon boundary: `engine.Engine` exposes project-owned value types, `internal/engine/moby.go` adapts the Moby client, and `internal/engine/readonly.go` wraps the interface for read-only mode. The Details pane in `internal/ui/details.go` already owns per-kind tabs, inspect loading, scrollable text, logs, and stats. The fake daemon and UI harness provide deterministic HTTP-backed tests.

Docker's container-top API returns a pair of dynamic values: `Titles []string` and `Processes [][]string`. The response is a snapshot, not a stream, and process rows can vary by daemon platform or command.

## Goals / Non-Goals

**Goals:**

- Add a project-owned process-list read operation without exposing Moby types outside the engine adapter.
- Integrate process snapshots with container identity, tab visibility, refresh invalidation, and stale-response protection.
- Render dynamic daemon columns safely within the existing Details pane and vertical scrolling model.
- Make stopped-container and daemon failure states ordinary in-pane states rather than mutation dialogs.
- Provide fake-daemon fixtures and tests covering engine mapping, request lifecycle, rendering, and narrow layouts.

**Non-Goals:**

- No streaming or periodic process polling.
- No custom `ps` arguments, sorting, filtering, signaling, or process attachment.
- No persistent process-history model.
- No changes to mutation safety or read-only authorization beyond adding a GET-like read.

## Decisions

### Use the Docker container-top API through the Engine interface

Add a read method that requests the default process listing with no arguments and returns a project-owned value containing titles and rows. The Moby adapter calls `ContainerTop` with empty options. This keeps Docker client types out of the UI and makes the request testable through the existing fake engine.

Alternative: invoke the Docker CLI or run `ps` through container exec. Both would add runtime dependencies, lose the daemon's structured response, and produce different semantics from Docker's native top endpoint.

### Keep process state separate from inspect state

The process result gets its own target identity, sequence, loading, error, validity, stale, capture time, rows, and vertical offset. Inspect state remains responsible only for inspect documents. This prevents a process refresh from invalidating unrelated detail data and allows late `/top` responses to be rejected independently.

Alternative: reuse `detailState` for all detail requests. That would couple different lifecycles and make it easier for a late inspect or process response to overwrite the wrong view.

### Fetch only when the Processes view is visible and needed

The frame reconciliation path checks whether Processes is the active container tab. It fetches when the target changes, the snapshot is invalid, or the tab is newly entered. The global `r` key invalidates process state regardless of the active tab; it only starts a request when Processes is visible. No timer is added for process refreshes.

Alternative: continuously poll while the tab is visible. That would add repeated process enumeration, especially costly over SSH or TCP, without changing the API's snapshot semantics.

### Invalidate old data immediately on identity changes

When the highlighted container changes, process rows and their capture time are cleared before the new request starts. A result is accepted only when both its target container ID and sequence match the current process request state. If a successful snapshot exists and the daemon disconnects, the rows remain visible with a stale/disconnected marker. If the container is known to have stopped, old rows are hidden and the stopped state is rendered instead.

Alternative: retain old rows while loading the next container. That risks showing one container's processes under another container's title.

### Render dynamic columns as a bounded single-line table

Compute column widths from returned titles and values, then shrink/truncate cells with the existing terminal-cell-aware fitting helper when the Details width is insufficient. Each process remains one visual row; rows are vertically scrollable with the existing Details controls. Missing row cells render empty and extra cells are handled without indexing beyond the returned titles.

Alternative: wrap rows or add horizontal scrolling. Wrapping makes vertical row navigation ambiguous; horizontal scrolling introduces another interaction model and is unnecessary for the first version.

### Treat stopped-container responses as ordinary view state

The UI checks the current container state before requesting processes and maps a not-running conflict returned during a race to the same explanatory stopped state. Other errors remain in the Processes pane and are retried by a later refresh.

Alternative: show every daemon response as a generic error. That would make an expected lifecycle transition look like a failure and would diverge from the existing Stats behavior for non-running containers.

## Risks / Trade-offs

- [Process arguments may contain secrets] -> Display the daemon response faithfully as explicitly requested, but do not copy process rows into jobs, logs, errors, or files; document the exposure in the user-facing behavior if needed.
- [Different daemons return different columns or row lengths] -> Preserve dynamic titles, tolerate missing or extra cells, and test variable-column responses.
- [A process snapshot can become stale immediately] -> Show capture time and stale/disconnected status; do not label the view as live.
- [The container may stop between state inspection and `/top`] -> Map the daemon's known not-running conflict to the stopped-container state.
- [Long commands can damage table geometry] -> Never wrap process rows; use bounded cell truncation and layout tests at narrow widths.
- [A daemon may not support process listing] -> Keep the error in the pane, preserve workspace usability, and allow retry with refresh.

## Migration Plan

No data migration or deployment migration is required. The change adds a read-only API method and a UI tab. Rollback consists of reverting the code and associated tests; existing container tabs and daemon operations remain unchanged.
