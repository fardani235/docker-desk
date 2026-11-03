## Purpose

Defines the deliberately limited compose support: containers are grouped by the project label
Docker already carries, and the group can be stopped or removed as a whole. No compose file is
ever read or executed.

## ADDED Requirements

### Requirement: Projects are derived from container labels only
The Compose kind SHALL list one project for each distinct value of the
`com.docker.compose.project` label found on existing containers. The program SHALL NOT read,
locate, parse or execute compose files and SHALL NOT run the compose tool.

#### Scenario: Grouping
- **WHEN** three containers carry the project label `shop` and two carry `blog`
- **THEN** the Compose kind lists `shop` with three containers and `blog` with two, each with its number of running containers

#### Scenario: No labelled containers
- **WHEN** no container carries the project label
- **THEN** the Compose kind says no compose projects were found

#### Scenario: Project details
- **WHEN** a project is highlighted
- **THEN** the Details pane lists its containers with service name and state, and shows the working directory and config-file path recorded in the labels as plain information

#### Scenario: Last container of a project is removed
- **WHEN** the last container carrying a project's label is removed
- **THEN** the project is no longer listed

### Requirement: Stop a whole project
The operator SHALL be able to stop every running container of the highlighted project at once.
The confirmation SHALL list the containers that will be stopped.

#### Scenario: Stopping
- **WHEN** the operator asks to stop a project with three running containers and confirms
- **THEN** one job stops those three containers, reports progress per container, and the project then shows zero running

#### Scenario: Nothing running
- **WHEN** no container of the project is running
- **THEN** the desk says there is nothing to stop and opens no confirmation

### Requirement: Remove a whole project
The operator SHALL be able to remove every container of the highlighted project at once,
without force. The confirmation SHALL list the containers that will be removed and SHALL state
that networks and volumes are left in place, naming those that carry the project's label.

#### Scenario: Removing a stopped project
- **WHEN** all containers of the project are stopped and the operator completes the typed confirmation
- **THEN** one job removes exactly the listed containers and the project disappears from the list

#### Scenario: Project still running
- **WHEN** any container of the project is running, paused or restarting
- **THEN** removal is refused, the running containers are named, and the operator is told to stop the project first

#### Scenario: Networks and volumes are left
- **WHEN** a project is removed
- **THEN** no network or volume is removed by that action

### Requirement: No compose lifecycle
The program SHALL NOT offer to create, start (`up`), tear down (`down`), build, pull or scale
a project.

#### Scenario: Looking for up or build
- **WHEN** the operator opens the action menu or the help for the Compose kind
- **THEN** only stop and remove are offered
