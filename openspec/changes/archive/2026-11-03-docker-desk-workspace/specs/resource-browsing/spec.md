## Purpose

Defines how the operator moves between the kinds of things Docker has, sees the objects of a
kind, and reads the details of the highlighted object without leaving the screen.

## ADDED Requirements

### Requirement: Five kinds of object
The Kinds pane SHALL list Containers, Images, Volumes, Networks and Compose projects, each with
the current number of objects of that kind.

#### Scenario: Picking a kind
- **WHEN** the highlight in the Kinds pane moves to a kind
- **THEN** the List pane immediately shows the objects of that kind, with no further key required

#### Scenario: Counts stay current
- **WHEN** an object is created or removed on the daemon
- **THEN** the count shown next to its kind is updated without operator action

### Requirement: Object lists show what the CLI listing shows
The List pane SHALL show one row per object with, at least: for containers — name, image,
state, status text, published ports, and live CPU and memory for running containers; for images
— repository, tag, short ID, size and age; for volumes — name, driver and whether it is in use;
for networks — name, driver, scope and number of attached containers; for compose projects —
project name, number of containers and how many are running. Stopped containers SHALL be
listed as well as running ones.

#### Scenario: Containers
- **WHEN** the Containers kind is picked
- **THEN** every container on the daemon, running or not, is listed with its state visibly distinguished

#### Scenario: Image with several tags
- **WHEN** an image carries more than one repository tag
- **THEN** each tag is listed as its own row

#### Scenario: Untagged image
- **WHEN** an image has no tag
- **THEN** it is listed with `<none>` in place of repository and tag

#### Scenario: Empty kind
- **WHEN** the daemon has no objects of the picked kind
- **THEN** the List pane says so instead of showing an empty table

### Requirement: Lists follow the daemon
Lists SHALL be refreshed when the engine reports a relevant event and also periodically, and
the operator SHALL be able to request a refresh. A refresh SHALL keep the highlight on the same
object when that object still exists.

#### Scenario: Object changes state elsewhere
- **WHEN** a container is stopped by another tool
- **THEN** its row shows the new state within two seconds without operator action

#### Scenario: Highlight survives a refresh
- **WHEN** the list is refreshed and rows are added or removed above the highlighted object
- **THEN** the same object remains highlighted

#### Scenario: Highlighted object disappears
- **WHEN** the highlighted object no longer exists after a refresh
- **THEN** the highlight moves to the nearest remaining row

### Requirement: Filtering a list
The operator SHALL be able to filter the current list by typing text; only rows containing that
text (case-insensitively) remain, and the active filter SHALL be displayed.

#### Scenario: Applying a filter
- **WHEN** the operator presses `/`, types `web` and presses Enter
- **THEN** only rows containing `web` are shown and the pane title shows the filter

#### Scenario: Clearing a filter
- **WHEN** the operator presses Escape in the List pane while a filter is active
- **THEN** the filter is removed and all rows are shown again

### Requirement: Details of the highlighted object
The Details pane SHALL always describe the object highlighted in the List pane. It SHALL offer
a readable summary and the daemon's complete inspect data, and for containers additionally
Logs and Stats views. Moving the highlight SHALL update the Details pane without any further
key.

#### Scenario: Container summary
- **WHEN** a container is highlighted
- **THEN** the summary shows at least its name, ID, image, state, start/finish times, exit code, restart policy, command, ports, mounts, networks with addresses, compose project and health when present

#### Scenario: Raw inspect
- **WHEN** the operator switches the Details pane to the inspect view
- **THEN** the full inspect document for the object is shown as indented JSON and can be scrolled

#### Scenario: Network summary
- **WHEN** a network is highlighted
- **THEN** the summary lists its driver, scope, subnets and the containers attached to it

#### Scenario: Volume summary
- **WHEN** a volume is highlighted
- **THEN** the summary shows its driver, mountpoint, labels and the containers that reference it

#### Scenario: Object vanishes while shown
- **WHEN** the highlighted object is removed while its details are displayed
- **THEN** the Details pane follows the new highlight and shows no error dialog
