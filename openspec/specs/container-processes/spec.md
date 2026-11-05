# container-processes Specification

## Purpose
Provides a read-only view of the processes currently reported inside a running container, using a manual snapshot so operators can investigate container activity without leaving Docker Desk.

## Requirements

### Requirement: Container process snapshots
The desk SHALL provide a Processes view for a highlighted container that displays a snapshot of the process titles and rows returned by the Docker daemon's default container process listing.

#### Scenario: Running container with processes
- **WHEN** the operator opens the Processes view for a running container
- **THEN** the desk requests a process snapshot and displays the daemon-provided column titles and process rows

#### Scenario: Daemon returns no processes
- **WHEN** the daemon successfully returns valid process titles with no process rows
- **THEN** the view says that no processes were reported and does not show an error

#### Scenario: Daemon returns variable columns
- **WHEN** the daemon returns process rows with columns different from the usual `docker top` output
- **THEN** the desk renders the returned titles and values without assuming fixed column names or positions

### Requirement: Snapshot refresh lifecycle
The desk SHALL use a manual snapshot lifecycle: fetch on first entry, fetch for a newly highlighted container, and fetch when the operator requests refresh while the Processes view is visible.

#### Scenario: Highlight changes
- **WHEN** the highlighted container changes while the Processes view is active
- **THEN** the prior snapshot is discarded immediately and a snapshot for the new container is requested

#### Scenario: Refresh while visible
- **WHEN** the operator presses the existing refresh key while the Processes view is visible
- **THEN** the current snapshot is invalidated and a new process snapshot is requested

#### Scenario: Refresh while hidden
- **WHEN** the operator presses the existing refresh key while another Details tab is visible
- **THEN** any cached process snapshot is invalidated without a hidden process request, and the next visit to Processes requests a fresh snapshot

#### Scenario: Snapshot is not polled
- **WHEN** the Processes view remains visible without operator input
- **THEN** the desk does not periodically request additional process snapshots

### Requirement: Process snapshot state and errors
The desk SHALL show explicit loading, stopped-container, error, and stale states for process snapshots without opening a mutation dialog.

#### Scenario: Snapshot loading
- **WHEN** a process snapshot request is in progress
- **THEN** the view indicates that the process list is loading

#### Scenario: Container is not running
- **WHEN** the operator opens Processes for a container that is stopped, paused, or otherwise not running
- **THEN** the view says that processes are available only for running containers and does not show a generic error dialog

#### Scenario: Container stops during request
- **WHEN** a container stops after the request begins and the daemon refuses the process listing
- **THEN** the view reports that the container is no longer running and remains usable

#### Scenario: General process-list failure
- **WHEN** the daemon returns an error other than a not-running condition
- **THEN** the error is displayed inside the Processes view and can be replaced by a later refresh

#### Scenario: Disconnect after successful snapshot
- **WHEN** the daemon disconnects after a successful snapshot has been displayed
- **THEN** the last snapshot remains visible with a clear stale or disconnected indication

#### Scenario: Container stops after successful snapshot
- **WHEN** the selected container stops after a successful process snapshot
- **THEN** the old process rows are hidden and the view shows that the container is no longer running

### Requirement: Process table navigation and safety
The Processes view SHALL support the existing Details-pane vertical scrolling controls and SHALL render each process as one visual row without allowing process data to corrupt adjacent panes.

#### Scenario: Scrolling process rows
- **WHEN** the operator uses the Details pane line, page, top, or bottom navigation keys
- **THEN** the process table scrolls vertically using the same navigation behavior as other scrollable Details views

#### Scenario: Long process values
- **WHEN** a title or process value is wider than the available Details pane
- **THEN** the value is truncated safely, the process remains one visual row, and the pane geometry remains intact

#### Scenario: Sensitive command arguments
- **WHEN** a daemon-provided process command contains an argument that may be sensitive
- **THEN** the desk displays the daemon-provided value without heuristic masking and does not persist it in jobs, logs, errors, or files

### Requirement: Default process listing only
The Processes view SHALL request the daemon's default process listing and SHALL not expose controls for custom process arguments, sorting, filtering, signaling, or attaching.

#### Scenario: Default request
- **WHEN** the desk requests a process snapshot
- **THEN** it sends no operator-selected custom process arguments

#### Scenario: Read-only process view
- **WHEN** the operator uses the Processes view
- **THEN** the desk provides observation and navigation only, with no process mutation or attachment action
