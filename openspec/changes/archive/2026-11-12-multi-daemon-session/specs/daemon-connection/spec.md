# Spec Delta

## RENAMED Requirements

- FROM: `### Requirement: Single daemon from the current Docker context`
- TO: `### Requirement: Launch daemon from the current Docker context`

## MODIFIED Requirements

### Requirement: Launch daemon from the current Docker context
The program SHALL choose the launch daemon with the Docker CLI's precedence: `--context`,
`--host`, `DOCKER_HOST`, `DOCKER_CONTEXT`, `currentContext` in the configuration file, then the
default context. Giving both `--host` and `--context` SHALL be refused. The launch daemon SHALL
be the first member of the session; daemons added and removed while running are governed by the
daemon-session capability.

#### Scenario: DOCKER_HOST is set
- **WHEN** `DOCKER_HOST` is set and no flag is given
- **THEN** the program uses that endpoint regardless of `DOCKER_CONTEXT` and of any configured context

#### Scenario: A named context is current
- **WHEN** the Docker configuration names a current context and nothing with higher precedence is given
- **THEN** the program uses that context's stored endpoint and reports that context's name

#### Scenario: The named context does not exist
- **WHEN** the selected context name has no stored metadata
- **THEN** the program exits with a non-zero status and a message naming the missing context and what selected it, without opening the workspace

#### Scenario: --host names the daemon
- **WHEN** the program is started with `--host URL` (or `-H URL`)
- **THEN** it uses that endpoint, whatever `DOCKER_HOST` and the configured context say, and reports that the flag chose it

#### Scenario: --context names the context
- **WHEN** the program is started with `--context NAME` (or `-c NAME`)
- **THEN** it uses that context's endpoint even if `DOCKER_HOST` is set, and reports that the flag chose it

#### Scenario: Both flags
- **WHEN** the program is started with both `--host` and `--context`
- **THEN** it exits with a non-zero status saying the two cannot be used together, without opening the workspace

### Requirement: Unreachable daemon is reported before the workspace opens
The program SHALL verify that the launch daemon answers before taking over the terminal. If it does
not answer within a bounded time, the program SHALL print a plain message that names the endpoint, how it was chosen, how it is reached and the reason, exit with a non-zero status, and
SHALL NOT open the workspace. The reason SHALL identify the actual failure where it can be
known, and SHALL list the possibilities rather than name one when it cannot.

#### Scenario: Socket does not exist
- **WHEN** the endpoint is a local socket whose file does not exist
- **THEN** the program prints that the Docker daemon is not reachable, the endpoint, and that the socket does not exist, then exits non-zero without drawing the workspace

#### Scenario: Permission denied
- **WHEN** the local socket exists but the user may not open it
- **THEN** the message names the endpoint and states that permission was denied

#### Scenario: Daemon does not answer
- **WHEN** the socket accepts no connection or the daemon does not answer within the time limit
- **THEN** the message names the endpoint and the failure, and the program exits non-zero without drawing the workspace

#### Scenario: SSH cannot log in
- **WHEN** the endpoint is `ssh://` and the ssh client fails to connect or authenticate
- **THEN** the message carries what ssh itself reported (for example permission denied, host key verification failed, connection refused) and the program exits non-zero without drawing the workspace

#### Scenario: No ssh client
- **WHEN** the endpoint is `ssh://` and no ssh client program can be started
- **THEN** the message says the ssh program was not found and how another one can be named

#### Scenario: SSH connects but the daemon's socket cannot be opened
- **WHEN** the SSH connection is established but the remote socket cannot be reached
- **THEN** the message says which it is when that can be determined — no socket at that path, not a socket, the remote user may not open it, or it lies in a directory the remote user may not look into — and otherwise lists the remaining possibilities, including an SSH server that forbids forwarding to sockets

#### Scenario: SSH connects and the daemon does not answer
- **WHEN** the SSH connection is established and nothing answers on the remote socket within the time limit
- **THEN** the message says that nothing answered, and does not present it as a refusal by the SSH server

#### Scenario: A fault in the client's own set-up
- **WHEN** a certificate file named by the configuration exists but cannot be used
- **THEN** the message names the file and the fault, and does not say that the daemon is unreachable

#### Scenario: TCP port refuses or the name does not resolve
- **WHEN** the endpoint is `tcp://` and the connection is refused, the host name cannot be resolved, or nothing answers in time
- **THEN** the message names the address and which of those happened

#### Scenario: TLS mismatch
- **WHEN** TLS is configured but the port does not speak TLS, or the port requires TLS and none is configured
- **THEN** the message says which side is missing TLS and how to supply or drop it

#### Scenario: Certificate problem
- **WHEN** the daemon's certificate is not signed by the configured authority or is not valid for the host name, or the daemon refuses or requires a client certificate
- **THEN** the message says which of those it is

#### Scenario: Interrupted while connecting
- **WHEN** the operator presses Ctrl+C before the workspace has opened, including while ssh is waiting for an answer
- **THEN** the program exits with a non-zero status, the ssh process it started is ended, and the workspace is not opened

#### Scenario: The message is safe to print
- **WHEN** the endpoint or context name contains control characters
- **THEN** the program refuses it without writing those characters to the terminal

### Requirement: Daemon identity is always visible
While the workspace is open the desk SHALL permanently display the focused daemon's endpoint as the
operator named it, its context name, and its engine's name and version, together with the
session's daemon strip, in every layout and behind no keystroke. Neither the connection state nor
any of these SHALL be dropped at any supported terminal size.

#### Scenario: Normal operation
- **WHEN** the workspace is open
- **THEN** the endpoint, context name and engine version of the focused daemon are visible on screen

#### Scenario: Focus moves to another daemon
- **WHEN** the operator moves focus to another member
- **THEN** the identity shown changes to that member's endpoint, context name and engine version

#### Scenario: A dialog is open
- **WHEN** a confirmation or any other overlay is displayed
- **THEN** the endpoint remains visible, and a confirmation for a mutation additionally repeats the endpoint in its own text

#### Scenario: SSH endpoint
- **WHEN** the endpoint is `ssh://` to another machine
- **THEN** the header shows the `ssh://` endpoint — not the local end of the tunnel — and a tag saying it is remote and reached over ssh

### Requirement: Connection loss is visible and recoverable
If the connection to the focused daemon is lost while the workspace is open, the program SHALL show
a disconnected state next to the endpoint, keep the last known data on screen marked as stale,
keep retrying, and resume live updates when the daemon answers again. Connection loss of an
unfocused daemon is reported as the daemon-session capability describes. When the daemon is
reached through SSH and it is the SSH connection that failed, the program SHALL additionally say so
and why, and SHALL re-establish the connection without asking anything on the terminal.

#### Scenario: Daemon stops while the desk is open
- **WHEN** the event stream ends or requests start failing with connection errors
- **THEN** the header shows that the daemon is disconnected and the desk keeps responding to keys

#### Scenario: Daemon comes back
- **WHEN** the daemon becomes reachable again
- **THEN** the disconnected indication clears, lists are refreshed and the event stream resumes without restarting the program

#### Scenario: The SSH connection drops
- **WHEN** the ssh connection carrying an `ssh://` endpoint ends while the workspace is open
- **THEN** the header shows the disconnected state, the status line says the SSH link is down and gives ssh's reason, and the desk keeps responding to keys

#### Scenario: The SSH connection can be re-established
- **WHEN** the remote machine accepts connections again and logging in needs no answer from the operator
- **THEN** the connection is re-established and live updates resume without restarting the program

#### Scenario: Re-establishing would need an answer
- **WHEN** logging in again would require a passphrase, a password or a host-key confirmation
- **THEN** nothing is asked on the terminal; the desk stays disconnected, says why, and remains usable for looking at the last known data and for quitting

### Requirement: Read-only mode
When started in read-only mode the whole session SHALL be read-only: the program SHALL refuse every
mutating action on every member, including daemons added later, before any request for it is sent
to the daemon, and SHALL show that it is in read-only mode at all times.

#### Scenario: Mutation attempted in read-only mode
- **WHEN** the operator triggers any action that would change or delete something, or open a shell
- **THEN** the program states that the desk is read-only and sends nothing to the daemon

#### Scenario: Daemon added in read-only mode
- **WHEN** the operator adds a daemon from the picker during a read-only session
- **THEN** the new daemon is read-only too, and the picker says so before the daemon is added

#### Scenario: Observation in read-only mode
- **WHEN** the operator browses lists, details, logs, stats and events
- **THEN** they behave exactly as in normal mode
