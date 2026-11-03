## Purpose

Defines which single Docker daemon the desk talks to, how the operator is kept aware of it, and
how the desk behaves when that daemon is unreachable, remote, or returns errors.

## ADDED Requirements

### Requirement: Single daemon from the current Docker context
The program SHALL talk to exactly one daemon for its whole lifetime: the one the operator's
current Docker context resolves to, using the same precedence as the Docker CLI — the
`DOCKER_HOST` environment variable, then the `DOCKER_CONTEXT` environment variable, then
`currentContext` in the Docker CLI configuration file, then the default local socket. The
program SHALL NOT offer any way to switch daemon while running.

#### Scenario: No configuration at all
- **WHEN** neither `DOCKER_HOST` nor `DOCKER_CONTEXT` is set and the Docker configuration names no current context
- **THEN** the program uses `unix:///var/run/docker.sock` and reports the context as `default`

#### Scenario: DOCKER_HOST is set
- **WHEN** `DOCKER_HOST` is set to a unix socket endpoint
- **THEN** the program uses that endpoint regardless of any configured context

#### Scenario: A named context is current
- **WHEN** the Docker configuration names a current context whose stored endpoint is a unix socket
- **THEN** the program uses that context's endpoint and reports that context's name

#### Scenario: The named context does not exist
- **WHEN** the current context name has no stored metadata
- **THEN** the program exits with a non-zero status and a message naming the missing context, without opening the workspace

### Requirement: Local daemons only
The program SHALL refuse to start when the resolved endpoint is not a local unix socket.

#### Scenario: Remote endpoint
- **WHEN** the resolved endpoint uses `tcp://`, `ssh://` or any scheme other than `unix://`
- **THEN** the program exits with a non-zero status, prints the endpoint and states that only local unix-socket daemons are supported, and does not open the workspace

### Requirement: Unreachable daemon is reported before the workspace opens
The program SHALL verify that the daemon answers before taking over the terminal. If it does
not answer within a bounded time, the program SHALL print a plain message that names the
endpoint and the reason, exit with a non-zero status, and SHALL NOT open the workspace.

#### Scenario: Socket does not exist
- **WHEN** the endpoint's socket file does not exist
- **THEN** the program prints that the Docker daemon is not reachable, the endpoint, and that the socket does not exist, then exits non-zero without drawing the workspace

#### Scenario: Permission denied
- **WHEN** the socket exists but the user may not open it
- **THEN** the message names the endpoint and states that permission was denied

#### Scenario: Daemon does not answer
- **WHEN** the socket accepts no connection or the daemon does not answer within the time limit
- **THEN** the message names the endpoint and the failure, and the program exits non-zero without drawing the workspace

### Requirement: Daemon identity is always visible
While the workspace is open it SHALL permanently display the daemon endpoint, the context name,
and the engine's name and version, in every layout and behind no keystroke.

#### Scenario: Normal operation
- **WHEN** the workspace is open
- **THEN** the endpoint, context name and engine version are visible on screen

#### Scenario: A dialog is open
- **WHEN** a confirmation or any other overlay is displayed
- **THEN** the endpoint remains visible, and a confirmation for a mutation additionally repeats the endpoint in its own text

### Requirement: Daemon errors never end the session
When the daemon rejects or fails a request, the program SHALL show the daemon's error message
and remain fully usable.

#### Scenario: A listing fails
- **WHEN** a list or inspect request returns an error
- **THEN** the affected pane shows the error text, the other panes keep working, and the request is retried on the next refresh

#### Scenario: A mutation fails
- **WHEN** a mutating request returns an error
- **THEN** the error message from the daemon is shown to the operator and kept with the job that issued it, and the workspace continues to accept input

### Requirement: Connection loss is visible and recoverable
If the connection to the daemon is lost while the workspace is open, the program SHALL show a
disconnected state next to the endpoint, keep the last known data on screen marked as stale,
keep retrying, and resume live updates when the daemon answers again.

#### Scenario: Daemon stops while the desk is open
- **WHEN** the event stream ends or requests start failing with connection errors
- **THEN** the header shows that the daemon is disconnected and the desk keeps responding to keys

#### Scenario: Daemon comes back
- **WHEN** the daemon becomes reachable again
- **THEN** the disconnected indication clears, lists are refreshed and the event stream resumes without restarting the program

### Requirement: Read-only mode
When started in read-only mode the program SHALL refuse every mutating action before any
request for it is sent to the daemon, and SHALL show that it is in read-only mode at all times.

#### Scenario: Mutation attempted in read-only mode
- **WHEN** the operator triggers any action that would change or delete something, or open a shell
- **THEN** the program states that the desk is read-only and sends nothing to the daemon

#### Scenario: Observation in read-only mode
- **WHEN** the operator browses lists, details, logs, stats and events
- **THEN** they behave exactly as in normal mode
