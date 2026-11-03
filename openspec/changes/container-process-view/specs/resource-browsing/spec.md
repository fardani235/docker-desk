# Spec Delta

## MODIFIED Requirements

### Requirement: Details of the highlighted object

The Details pane SHALL always describe the object highlighted in the List pane. It SHALL offer a readable summary and the daemon's complete inspect data, and for containers additionally Logs, Stats, Processes, and Inspect views. Moving the highlight SHALL update the Details pane without any further key.

#### Scenario: Container summary
- **WHEN** a container is highlighted
- **THEN** the summary shows at least its name, ID, image, state, start/finish times, exit code, restart policy, command, ports, mounts, networks with addresses, compose project and health when present

#### Scenario: Raw inspect
- **WHEN** the operator switches the Details pane to the inspect view
- **THEN** the full inspect document for the object is shown as indented JSON and can be scrolled

#### Scenario: Container processes
- **WHEN** the operator switches the Details pane to the Processes view for a container
- **THEN** the view shows the current process snapshot reported by the daemon, or an explicit loading, empty, stopped-container, stale, or error state

#### Scenario: Network summary
- **WHEN** a network is highlighted
- **THEN** the summary lists its driver, scope, subnets and the containers attached to it

#### Scenario: Volume summary
- **WHEN** a volume is highlighted
- **THEN** the summary shows its driver, mountpoint, labels and the containers that reference it

#### Scenario: Object vanishes while shown
- **WHEN** the highlighted object is removed while its details are displayed
- **THEN** the Details pane follows the new highlight and shows no error dialog
