# Spec Delta

## Purpose

Defines how the operator creates a container from the desk: the form and the fields it accepts,
how repeatable entries are written and checked, how a missing image is handled, and what
happens when the new container is pulled, created and started.

## ADDED Requirements

### Requirement: Creating a container starts from the Containers kind
The operator SHALL be able to open the create form from the Containers kind with a key or from
the action menu, whether or not rows are marked. Opening it SHALL send nothing. In read-only
mode the action SHALL be refused: no form opens, nothing is sent, and the status line says
why.

#### Scenario: Opening the form
- **WHEN** the operator presses the create key while the Containers kind is shown
- **THEN** the create form opens and no request has been sent to the daemon

#### Scenario: A selection is marked
- **WHEN** two container rows are marked and the operator opens the create form
- **THEN** the form opens and the selection is unaffected

#### Scenario: Read-only
- **WHEN** the desk is in read-only mode and the operator asks to create a container
- **THEN** no form opens, nothing is sent, and the status line says read-only mode

### Requirement: The form collects seven things
The form SHALL collect, in order: the image reference (required), the container name
(optional), published ports, environment variables, mounts, restart policy and the command
(optional). Ports, environment and mounts SHALL be repeatable entries. The restart policy
SHALL be one of `no`, `always`, `unless-stopped` or `on-failure` with an optional retry limit.

#### Scenario: Image only
- **WHEN** the operator types an image and leaves every other field as it is
- **THEN** the plan has no published ports, no environment, no mounts, the `no` restart policy,
  and the image's own command

#### Scenario: Name left empty
- **WHEN** no name is given
- **THEN** the daemon chooses the container's name and the confirmation says so

#### Scenario: Restart policy
- **WHEN** the operator chooses `on-failure` and a retry limit of 3
- **THEN** the container is created with that policy and limit

### Requirement: Each repeatable entry has one syntax
A port entry SHALL be written `[host-address:][host-port:]container-port[/tcp|udp]`. An
environment entry SHALL be written `NAME=value`, the value being everything after the first
`=`. A mount entry SHALL be written `source:destination[:ro]`, with an absolute destination and
a source that is a host path when it begins with `/` and a volume name otherwise.

#### Scenario: Ports
- **WHEN** the operator enters `8080:80`, `127.0.0.1:5432:5432` and `53:53/udp`
- **THEN** the plan publishes two TCP ports and one UDP port with exactly those addresses

#### Scenario: Environment
- **WHEN** the operator enters `LOG_LEVEL=info`
- **THEN** the plan sets the environment variable `LOG_LEVEL` to `info`

#### Scenario: Mounts
- **WHEN** the operator enters `/srv/data:/data:ro` and `cache:/var/cache`
- **THEN** the plan makes the first a read-only bind mount from a host path and the second a
  writable named volume

#### Scenario: An entry that does not parse
- **WHEN** an entry does not follow its field's syntax
- **THEN** the plan is not built, the form says which entry is wrong, and nothing is sent

### Requirement: The form is edited from the keyboard
`Tab` and `Shift+Tab` SHALL move between fields, wrapping at the ends. `↑` and `↓` SHALL move
between the rows of the focused field, and `←` and `→` SHALL change the restart policy. `Ctrl+N`
SHALL add an entry and `Ctrl+X` SHALL remove the focused entry. `Enter` SHALL submit and `Esc`
SHALL close the form and send nothing. The form SHALL show these keys.

#### Scenario: Adding and removing entries
- **WHEN** the operator presses `Ctrl+N` twice in the ports field and `Ctrl+X` once
- **THEN** the field has two entries and the focused one was the one removed

#### Scenario: Moving between fields
- **WHEN** the operator presses `Tab` from the last field
- **THEN** focus returns to the first field, and `Shift+Tab` goes the other way

#### Scenario: Closing the form
- **WHEN** the operator presses `Esc`
- **THEN** the form closes, nothing is sent and no job is recorded

### Requirement: Nothing is sent until the values are usable
On `Enter` the desk SHALL check every value and SHALL send nothing if one is unusable: an
invalid image reference, an unusable or already-used name, a port outside 1–65535 or repeated
on the same host address and protocol, an environment name that is empty or contains `=`, a
relative mount destination, or a command that does not parse. The form SHALL stay open, say
what is wrong, and put focus on the field at fault.

#### Scenario: Image without a tag
- **WHEN** the operator enters `alpine`
- **THEN** the plan uses `alpine:latest` and the confirmation shows that exact reference

#### Scenario: Port out of range
- **WHEN** an entry is `70000:80`
- **THEN** nothing is sent and the form says the port must be between 1 and 65535

#### Scenario: Name already used
- **WHEN** the name entered belongs to an existing container on the daemon
- **THEN** nothing is sent and the form says the name is taken

#### Scenario: Environment without a name
- **WHEN** an entry is `=value`
- **THEN** nothing is sent and the form says an environment entry needs a name

#### Scenario: Relative mount destination
- **WHEN** an entry is `/srv/data:data`
- **THEN** nothing is sent and the form says the destination must be an absolute path

#### Scenario: Command that does not parse
- **WHEN** the command's quoting cannot be parsed into arguments
- **THEN** nothing is sent and the form says what is wrong with it

### Requirement: The form is the confirmation
The form SHALL state the daemon, the complete configuration that will be sent — including the
name or that the daemon will assign one, every port, environment entry, mount, the restart
policy and the command — and whether the image is present or will be pulled first. It SHALL
state that the container will be created and started. Nothing SHALL be sent until `Enter` on a
form that has been on screen long enough to read.

#### Scenario: The summary before submitting
- **WHEN** the operator fills the form
- **THEN** the summary names the daemon and restates every value exactly as it will be sent

#### Scenario: Enter typed ahead
- **WHEN** the create key and `Enter` arrive before the form has been drawn
- **THEN** nothing is sent and the form stays open

#### Scenario: An image that is not present
- **WHEN** the typed image is not on the daemon
- **THEN** the summary says it will be pulled first, and the plan contains that pull

### Requirement: A missing image is pulled before the container is created
When the reference is not present on the daemon the plan SHALL pull exactly that reference,
never all tags, before creating the container. When the image is present no pull SHALL be
sent.

#### Scenario: Pull then create
- **WHEN** the operator creates from an image the daemon does not have
- **THEN** one job pulls the exact reference, then creates the container, then starts it

#### Scenario: Image already present
- **WHEN** the image is already on the daemon under that reference
- **THEN** the job sends no pull

#### Scenario: The pull fails
- **WHEN** the pull fails, for example because the registry does not have the reference
- **THEN** the container is not created or started, and the job fails with the daemon's message

### Requirement: Create and start are one job
Submitting the form SHALL start one background job that pulls when needed, creates the
container and starts it, and SHALL show the pull's progress when there is a pull. When the
start succeeds the new container SHALL appear in the Containers list, running, without operator
action.

#### Scenario: Created and running
- **WHEN** the job finishes successfully
- **THEN** the new container is listed as running and the job is shown as succeeded

#### Scenario: The daemon refuses the create
- **WHEN** the daemon returns an error for the create, for example an unusable host port
- **THEN** nothing is started, the job fails with the daemon's message, and the desk stays usable

#### Scenario: The start fails
- **WHEN** the container is created but the start is refused
- **THEN** the container exists in the `created` state, the job says it was created but not
  started, and the list shows it

#### Scenario: A step is skipped
- **WHEN** a step the remaining steps depend on fails
- **THEN** those remaining steps are not attempted and the job output names them and the step
  they waited on

#### Scenario: No name was given
- **WHEN** the daemon assigns the name and the job succeeds
- **THEN** the job output names the container the daemon created

### Requirement: Environment values are not recorded
The job's title, target, output and the status line SHALL NOT contain environment values. The
form SHALL be the only place they are drawn.

#### Scenario: A secret in the environment
- **WHEN** the operator enters `TOKEN=s3cret` and the container is created
- **THEN** the job and the status line name the container and its image but never show
  `s3cret`, while the form does show it before submission

### Requirement: Creating changes nothing that already exists
A create plan SHALL send only the pull for its own image and the create and start of its own
container. It SHALL NOT start, stop or remove any other container, volume, network or image.

#### Scenario: Other objects are untouched
- **WHEN** a container is created on a daemon with running containers and unused volumes
- **THEN** every one of them keeps its state and nothing else is removed
