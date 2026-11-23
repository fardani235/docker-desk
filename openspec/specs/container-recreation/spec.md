# container-recreation Specification

## Purpose

Defines re-creating a container on a newer build of the image its tag names: when the desk
offers it, the configuration it carries or refuses, how the new container takes the old one's
place without deleting anything, and what the operator is told.

## Requirements

### Requirement: A moved build offers a re-create
On Containers › Changes, when the comparison has found that the tag names a newer build for
the same platform and the container's configuration is reproducible, the tab SHALL offer `A`
and name the build it would create. Pressing it SHALL send nothing before the operator
confirms. Read-only mode SHALL refuse it before any dialog. It SHALL act on the highlighted
container only, and SHALL refuse while the kind has a selection.

#### Scenario: A moved tag
- **WHEN** the Changes tab shows that the tag names a newer build of the same platform and the configuration is reproducible
- **THEN** the tab says the operator can re-create the container with `A` and names the build it would create

#### Scenario: The tag names the image that runs
- **WHEN** the tag still names the image the container runs
- **THEN** the tab offers no re-create and says there is nothing to change

#### Scenario: Read-only
- **WHEN** the desk runs with `--read-only` and the operator presses `A`
- **THEN** no dialog opens, no request is sent, and the status line says read-only mode

#### Scenario: A selection is marked
- **WHEN** one or more containers are marked and the operator presses `A` on the tab
- **THEN** no dialog opens and the status line says a re-create acts on one container at a time

### Requirement: The container's own settings are carried
The plan SHALL reproduce the container's own configuration as the daemon reports it: the image
reference, the name, the command and entrypoint, every environment entry with its value, the
working directory, user, hostname, TTY setting, labels, the health check, and the stop signal
and grace period. A setting the container does not set SHALL stay unset rather than be
invented.

#### Scenario: Environment
- **WHEN** the container has `LOG_LEVEL=info` and `TOKEN=s3cret`
- **THEN** the new container is created with both entries and those exact values

#### Scenario: Command and entrypoint
- **WHEN** the container overrides the image's entrypoint and command
- **THEN** the new container runs with the same entrypoint and command, not the image's

#### Scenario: Health check and stop behaviour
- **WHEN** the container sets a health check and a stop signal with a grace period
- **THEN** the new container is created with the same health check and stop behaviour

#### Scenario: Labels
- **WHEN** the container carries labels, including a compose project and service
- **THEN** the new container carries every one of them unchanged

### Requirement: Its connections and storage are carried
The plan SHALL reproduce published ports with their host addresses and host ports, ports that
are exposed without being published, bind and volume mounts with their read-only flag, every
user-defined network the container is attached to with its aliases, and the container's network
mode. An anonymous volume SHALL be reused by the name the daemon gave it, never replaced by a
new empty one.

#### Scenario: A fixed host port
- **WHEN** the container publishes `8080:80`
- **THEN** the new container publishes the same host address, host port and protocol

#### Scenario: An exposed-only port
- **WHEN** a port is exposed in the image or config but not published
- **THEN** the new container exposes it and does not publish it

#### Scenario: An anonymous volume
- **WHEN** the container mounts an anonymous volume the daemon named
- **THEN** the new container mounts that same volume, so its data is not lost

#### Scenario: A read-only bind
- **WHEN** the container mounts a host path read-only
- **THEN** the new container mounts the same path read-only

#### Scenario: A named volume in the daemon's default mode
- **WHEN** the container mounts a named volume the daemon reports with its default mode, without an explicit mode on the mount
- **THEN** the new container mounts the same volume under the same destination and the re-create is offered

#### Scenario: Several networks
- **WHEN** the container is attached to the compose project's network with an alias
- **THEN** the new container is attached to the same network with the same alias

### Requirement: Its restart policy and resource limits are carried
The plan SHALL reproduce the restart policy with its retry limit, and the container's memory,
swap and reservation limits, CPU shares, quota, period and cpuset, process limit, and shared
memory size. A limit the daemon reports as unset SHALL stay unset.

#### Scenario: Restart on failure
- **WHEN** the container uses `on-failure` with a retry limit of 3
- **THEN** the new container uses the same policy and limit

#### Scenario: A memory limit
- **WHEN** the container has a memory limit and a CPU quota
- **THEN** the new container has the same limit and quota

### Requirement: A setting outside the model refuses the action
When the container carries a setting that changes what it may do to the host or how it is
isolated, or a fixed network address, and the plan does not reproduce it, the desk SHALL refuse
the re-create and name every such setting. No confirmation SHALL be accepted and nothing SHALL
be sent.

#### Scenario: A privileged container
- **WHEN** the container runs privileged
- **THEN** the dialog lists privilege among the settings it cannot reproduce and offers no way to proceed

#### Scenario: A custom runtime
- **WHEN** the container was created with a runtime such as a GPU runtime
- **THEN** the dialog names that runtime and refuses

#### Scenario: A borrowed network namespace
- **WHEN** the container uses another container's network namespace
- **THEN** the dialog names the namespace mode and refuses

#### Scenario: A fixed address
- **WHEN** the container's network attachment has a fixed address configured
- **THEN** the dialog names the address setting and refuses

#### Scenario: A private label on a volume
- **WHEN** a named volume was mounted with the private label `Z`
- **THEN** the dialog names the mount option and refuses

#### Scenario: A label on a bind
- **WHEN** a bind mount was created with the shared label `z` or the private label `Z`
- **THEN** the dialog names the mount option and refuses

#### Scenario: Several unsupported settings
- **WHEN** the container has a capability, a device and a log driver option
- **THEN** the dialog names all three, not only the first

### Requirement: Environment values are carried but never displayed
The plan SHALL carry environment values because the daemon needs them. The confirmation, its
effects, the job, the job output, the status line and every error SHALL name variables and
counts only; no environment value SHALL appear in any of them.

#### Scenario: A secret
- **WHEN** the container sets `TOKEN=s3cret`
- **THEN** the confirmation says one environment entry is carried, naming `TOKEN` at most, and no output contains `s3cret`

#### Scenario: A failed create
- **WHEN** the daemon refuses the create and reports its reason
- **THEN** the job's error contains the daemon's text and no environment value

### Requirement: The plan replaces the container without deleting anything
The plan SHALL send, in order: a stop when the container is active, a rename of the old
container to a free name derived from its own and the old identifier, a create of the new
container under the original name from the pinned build, and a start of the new container.
Each step SHALL depend on the one before it. No removal request SHALL be part of the plan, and
the old container SHALL remain, stopped, under the derived name.

#### Scenario: A running container
- **WHEN** the container runs and the operator confirms
- **THEN** one job stops it, renames it, creates the new container under the original name, and starts it

#### Scenario: A stopped container
- **WHEN** the container is already stopped
- **THEN** the plan sends no stop, only the rename, create and start

#### Scenario: The derived name is taken
- **WHEN** the derived name already belongs to another container
- **THEN** a different free name is chosen and shown in the confirmation

#### Scenario: Nothing is deleted
- **WHEN** the plan is built
- **THEN** it contains no request that removes a container, volume, network or image

#### Scenario: A step is skipped
- **WHEN** a step fails
- **THEN** every step that depends on it is skipped and the job names the step it waited on

### Requirement: The build is pinned before anything is sent
The confirmation SHALL name the tag and the build it named when the dialog opened. Before the
first step the plan SHALL re-check that the tag still names that build; when it names another,
no request SHALL be sent. A reference that is a digest or an image ID, a reference that no
longer resolves, or a build for another platform SHALL NOT be offered.

#### Scenario: The tag moves again
- **WHEN** the tag names another build by the time the confirmed plan starts
- **THEN** the job fails before its first step, nothing is stopped, renamed or created, and it says the tag moved

#### Scenario: Another platform
- **WHEN** the tag now names a build for a platform other than the container's
- **THEN** no re-create is offered and the tab says the platforms differ

#### Scenario: A pinned reference
- **WHEN** the container was created from a digest or an image ID
- **THEN** no re-create is offered

### Requirement: The confirmation is typed and names the effect
The confirmation SHALL require the operator to type `recreate` and press Enter, SHALL be armed
as other confirmations are, and SHALL name the daemon, the container and its short ID, both
image references and short IDs, the derived name the old container will take, a summary of the
configuration carried with environment names only, and that the old container is kept. Any
other word SHALL send nothing.

#### Scenario: The word matches
- **WHEN** the operator types `recreate` and presses Enter
- **THEN** the job starts with the plan that was shown

#### Scenario: The word does not match
- **WHEN** the operator types anything else and presses Enter
- **THEN** nothing is sent and the dialog says the text does not match

#### Scenario: Enter alone
- **WHEN** the confirmation is shown and the operator presses Enter or `y` without typing the word
- **THEN** nothing is sent

#### Scenario: Typed ahead
- **WHEN** the action key and the confirmation arrive in quick succession before the dialog is drawn
- **THEN** nothing is sent and the dialog stays open

### Requirement: A failed step stops the rest and states what remains
A failure SHALL prevent every dependent step and SHALL be reported with the state it leaves:
after a failed stop, the container is as it was; after a failed rename, it remains stopped under
its original name and nothing was created; after a failed create, it remains stopped under the
derived name and nothing was started; after a failed start, the new container exists in the
created state and the old one is as described. No step SHALL delete anything.

#### Scenario: The stop is refused
- **WHEN** the daemon refuses the stop
- **THEN** no rename, create or start is attempted and the job says so

#### Scenario: The rename is refused
- **WHEN** the daemon refuses the rename
- **THEN** no container is created, the old one keeps its name, and the job says so

#### Scenario: The create is refused
- **WHEN** the daemon refuses the create
- **THEN** nothing is started, the old container stays stopped under the derived name, and the job names that name

#### Scenario: The start is refused
- **WHEN** the container is created but the start is refused
- **THEN** the job says it was created but not started and names both containers

#### Scenario: Cancelled between steps
- **WHEN** the operator cancels the job after the rename
- **THEN** the job reports the steps already done, including the derived name, and sends nothing further

### Requirement: After success the desk and the tab agree
When the job succeeds, the new container SHALL run under the original name, the old one SHALL
remain stopped under the derived name, and the job SHALL name both. The list SHALL update from
the daemon's events. On the next visit the Changes tab SHALL offer no re-create, because the tag
now names the image the new container runs.

#### Scenario: Success
- **WHEN** the job finishes successfully
- **THEN** one container runs under the original name, one stopped container carries the derived name, and the job names both

#### Scenario: The next visit
- **WHEN** the operator returns to the Changes tab for the new container
- **THEN** the tab says the tag names the image it runs and offers no re-create

### Requirement: A compose-managed container keeps its grouping and says what remains
The plan SHALL carry every label, including the compose project, service, config hash and
working directory. The confirmation SHALL state that the old container remains, that compose
aware tools may list it under the same service, and that the compose file is not read.

#### Scenario: Compose labels
- **WHEN** the container carries compose labels
- **THEN** the new container carries the same labels and appears in the same project group

#### Scenario: The warning
- **WHEN** the container belongs to a compose project
- **THEN** the confirmation says the old container remains under the derived name and the compose file is not consulted
