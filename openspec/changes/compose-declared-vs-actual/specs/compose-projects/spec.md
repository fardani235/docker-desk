# Spec Delta

## MODIFIED Requirements

### Requirement: Projects are derived from container labels only

The Compose kind SHALL list one project for each distinct value of the
`com.docker.compose.project` label found on existing containers. The program SHALL NOT run the
compose tool and SHALL NOT create, start, scale, build, pull or tear down a project. It SHALL
read a compose file only as the requirement below allows, and SHALL never treat such a file as
the project's definition.

#### Scenario: Grouping
- **WHEN** three containers carry the project label `shop` and two carry `blog`
- **THEN** the Compose kind lists `shop` with three containers and `blog` with two, each with its number of running containers

#### Scenario: No labelled containers
- **WHEN** no container carries the project label
- **THEN** the Compose kind says no compose projects were found

#### Scenario: Project details
- **WHEN** a project is highlighted
- **THEN** the Details pane lists its containers with service name and state, shows the working
  directory and config-file paths recorded in the labels, and says that no file has been read
  yet

#### Scenario: Last container of a project is removed
- **WHEN** the last container carrying a project's label is removed
- **THEN** the project is no longer listed

## ADDED Requirements

### Requirement: A project's file is read only when asked

The desk SHALL read no compose file until the operator asks for the Declared comparison. It SHALL
read a regular file of at most 1 MiB at the path the labels record, and SHALL NOT render any byte
of it: only the comparison and, on failure, one refusal sentence.

#### Scenario: Nothing read before the operator asks
- **WHEN** a project is highlighted, the list refreshes, or the tab is opened
- **THEN** the path is shown and no file is opened

#### Scenario: A path that is not a compose file
- **WHEN** the recorded path cannot be opened, is not a regular file, exceeds 1 MiB, or does not
  parse
- **THEN** the tab states why in one sentence and shows no rows and no file contents

### Requirement: The comparison is made on this machine only

The comparison SHALL run only when the daemon was reached over a local Unix socket. Over SSH or
TCP the tab SHALL say the path belongs to the machine that ran compose and SHALL read nothing.
`--no-compose-files` SHALL forbid the read and name the flag. Read-only mode SHALL permit it, as
it only reads.

#### Scenario: A daemon on another machine
- **WHEN** the daemon was reached over SSH or TCP and the operator asks for the comparison
- **THEN** the tab says the path is on the machine that ran compose and no file is opened

#### Scenario: Forbidden by flag
- **WHEN** the desk runs with `--no-compose-files`
- **THEN** the comparison is refused and the refusal says which flag forbade it

#### Scenario: Read-only mode
- **WHEN** the desk runs with `--read-only` and the operator asks for the comparison
- **THEN** the comparison runs, because it only reads

### Requirement: The declared side is what the file says now

The comparison SHALL use the file found at the recorded path when the operator asks, and the tab
SHALL state that the file is compared as it is now: nothing records its contents when the
containers were created. The file's write time SHALL be reported against the newest container's
creation time as a fact, never as a verdict.

#### Scenario: The file was edited after the containers started
- **WHEN** the file's write time is later than the newest container's creation time
- **THEN** the tab reports both times and that the file was written after the containers were
  created, without claiming the file is wrong

### Requirement: Several files are read in the order the label names them

When the config-files label names more than one file, service names SHALL be the union of those
files, a service's image SHALL come from the last file that declares one, and the tab SHALL name
every file read.

#### Scenario: Several files
- **WHEN** the config-files label names two paths
- **THEN** both are read in the order listed, the tab names both, and a service declared in
  either is listed once

### Requirement: What cannot be compared is said, not guessed

A declared image value containing `${` SHALL be reported as a template and not compared, because
the desk does not interpolate. A declared service carrying `profiles` SHALL NOT be called
missing. Containers carrying `com.docker.compose.oneoff=True` SHALL be excluded from the
comparison. Runtime scale, ports, volumes, environment, command and health checks SHALL NOT be
compared. The recorded `com.docker.compose.config-hash` SHALL be shown as evidence and SHALL NOT
be recomputed.

#### Scenario: An interpolated image
- **WHEN** a service declares `image: app:${TAG:-latest}`
- **THEN** the row says the value is a template and is not compared, and no drift is claimed

#### Scenario: A profiled service
- **WHEN** a declared service has no container and declares `profiles`
- **THEN** the row says it is declared with profiles and the desk cannot know if they were
  enabled, and it is not counted as missing

#### Scenario: A one-off container
- **WHEN** a container of the project carries `com.docker.compose.oneoff=True`
- **THEN** it is not compared against a declared service

### Requirement: Services and images are compared

The comparison SHALL report, for each service, whether the file declares it, whether a container
of the project carries it, and the verdict match, differs, declared only or present only. An
image SHALL be compared only when the declared value is a literal and normalises to the
container's image reference; otherwise the row SHALL say why it is not compared.

#### Scenario: A declared service with no container
- **WHEN** the file declares `db` and no container of the project carries service `db`
- **THEN** the row is declared only and the tab lists it first

#### Scenario: A container the file does not declare
- **WHEN** a container carries a service name no file service declares
- **THEN** the row is present only and the tab says the file does not declare it

#### Scenario: A moved image tag
- **WHEN** a service declares `app:1.2` and its container runs `app:1.3`
- **THEN** the row is differs (tag) and both references are shown
