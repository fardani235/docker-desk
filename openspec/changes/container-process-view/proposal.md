# Proposal

## Why

Docker Desk already lets an operator inspect a container, follow its logs, and watch resource statistics, but it does not show which processes are currently running inside the container. Adding a read-only process snapshot gives the operator a direct answer to that operational question without leaving the desk or opening a separate Docker CLI session.

## What Changes

- Add a `Processes` tab to the container Details pane.
- Fetch the daemon's default process listing from Docker's container-top API.
- Preserve and render the column titles and rows returned by the daemon.
- Use a manual snapshot model: load on first entry, reload when the highlighted container changes, and reload on `r` while visible.
- Invalidate a cached snapshot on `r` when the tab is hidden, without making a hidden request; fetch when the tab is next opened.
- Show explicit loading, empty, stopped-container, stale/disconnected, and error states.
- Support the existing Details-pane vertical scrolling controls.
- Render one process per visual row, truncating cells safely rather than wrapping rows.
- Keep the feature read-only: no custom `ps` arguments, sorting, filtering, process signals, attachment, or persistence.
- Extend the fake Docker engine and automated tests with deterministic process-list responses.

## Capabilities

### New Capabilities

- `container-processes`: Read-only inspection of the current processes reported for a running container, including snapshot lifecycle, rendering, and non-running/error states.

### Modified Capabilities

- `resource-browsing`: Extend the container Details pane with a Processes view alongside Info, Logs, Stats, and Inspect.

## Impact

- Affected engine abstraction and Docker client adapter: add a project-owned process-list value and a read method backed by `/containers/{id}/top`.
- Affected container Details UI state, tab navigation, refresh handling, scrolling, and narrow-pane rendering.
- Affected fake daemon fixtures and UI/engine/end-to-end tests.
- No breaking changes to existing commands, mutation behavior, read-only guarantees, or stored data.
