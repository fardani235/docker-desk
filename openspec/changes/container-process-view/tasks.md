# Tasks

## 1. Engine contract and Docker adapter

- [ ] 1.1 Add a project-owned process-list value containing daemon-provided titles and rows, plus a read method to the engine interface; verify the engine package compiles and existing implementations identify all required methods.
- [ ] 1.2 Implement the process-list read through the Moby container-top API with no custom arguments, preserving daemon errors and read-only compatibility; verify engine tests cover successful mapping, empty rows, variable columns, missing containers, stopped containers, and unavailable daemons.
- [ ] 1.3 Extend the fake engine with deterministic `/containers/{id}/top` responses and request observability, including running, empty, stopped, missing, variable-column, and failure cases; verify the fake's HTTP behavior through focused tests.

## 2. Details tab and snapshot lifecycle

- [ ] 2.1 Add `Processes` to the container Details tabs and route tab navigation, rendering, and Details-pane hints without exposing it for non-container kinds; verify layout and tab-cycle tests cover every kind at the supported terminal sizes.
- [ ] 2.2 Add process snapshot state keyed by container identity and request sequence, with loading, valid, invalid, error, capture-time, and stale/disconnected states; verify unit/UI tests reject late responses and never display one container's rows under another container.
- [ ] 2.3 Integrate manual fetch triggers: initial visible entry, container highlight change while visible, visible `r`, and hidden `r` invalidation without a hidden request; verify request counts and cache behavior with the fake engine.
- [ ] 2.4 Map stopped-container state and not-running races to an explanatory in-pane message, retain successful rows only for disconnect staleness, and hide rows after the selected container stops; verify stopped, race, disconnect, and recovery scenarios without dialogs.

## 3. Process table rendering and navigation

- [ ] 3.1 Render dynamic titles and rows as a bounded single-line table with terminal-cell-aware truncation, tolerant missing or extra cells, capture-time/status text, and an empty-result state; verify variable-column, long-command, empty, and geometry tests.
- [ ] 3.2 Reuse the Details vertical scrolling controls for process rows and keep process data out of jobs, logs, errors, and files; verify line/page/top/bottom navigation and sensitive-command display behavior.

## 4. Documentation and integration verification

- [ ] 4.1 Update the user-facing key/help documentation to describe the Processes tab, manual snapshot semantics, refresh behavior, stopped-container state, and the fact that command-line arguments are displayed faithfully; verify every documented control matches the implemented key handling.
- [ ] 4.2 Add an end-to-end UI scenario that opens Processes for a running container, shows returned rows, refreshes them, switches containers, handles a stopped container, and preserves pane geometry; verify the scenario with the existing pseudo-terminal or UI harness.
- [ ] 4.3 Run the complete Go test suite and strict OpenSpec validation; verify no existing container operations, read-only guarantees, or layouts regress.
