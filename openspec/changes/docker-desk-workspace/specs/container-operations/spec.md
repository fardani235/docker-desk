## Purpose

Defines what the operator can do to and learn about a container from the desk: lifecycle
actions, logs with live follow, live resource figures, and an interactive shell.

## ADDED Requirements

### Requirement: Lifecycle actions
The operator SHALL be able to start, stop, restart, pause, unpause, kill and remove the
highlighted container. Each SHALL follow the mutation-safety rules and run as a job.

#### Scenario: Stop a running container
- **WHEN** the operator asks to stop a running container and confirms
- **THEN** a stop request is sent for that container, a job tracks it, and the list shows the container as exited once the engine reports it

#### Scenario: Start a stopped container
- **WHEN** the operator asks to start an exited container and confirms
- **THEN** the container is started and shown as running

#### Scenario: Pause and unpause
- **WHEN** the operator pauses a running container and later unpauses it, confirming each
- **THEN** the container is shown as paused and then as running again

#### Scenario: Kill
- **WHEN** the operator asks to kill a running container and confirms
- **THEN** the container is sent SIGKILL and shown as exited

#### Scenario: Remove a stopped container
- **WHEN** the operator asks to remove an exited container and completes the typed confirmation
- **THEN** the container is removed without force and without removing its volumes, and disappears from the list

#### Scenario: Action does not apply to the current state
- **WHEN** the operator presses the key of an action that cannot apply to the container's state (for example start on a running container, pause on a stopped one)
- **THEN** the desk says why it does not apply and opens no confirmation

### Requirement: Logs with live follow
The Details pane SHALL show the highlighted container's recent log output (stdout and stderr)
and SHALL follow new output live. The operator SHALL be able to scroll back, which suspends
following, and resume following. Following SHALL also work for a container that is not running
(showing its last output) and SHALL pick up again if the container is restarted.

#### Scenario: Following
- **WHEN** the logs view is shown for a running container that writes a line
- **THEN** the line appears at the end of the view without operator action

#### Scenario: Scrolling back
- **WHEN** the operator scrolls up in the logs view
- **THEN** the view holds its position while new lines keep arriving, and indicates that it is no longer following

#### Scenario: Stopped container
- **WHEN** the logs view is shown for an exited container
- **THEN** its last log lines are shown

#### Scenario: Control sequences in logs
- **WHEN** a log line contains terminal control sequences
- **THEN** they are neutralised so they cannot move the cursor or corrupt other panes, while colour sequences MAY be preserved

#### Scenario: Switching container
- **WHEN** the highlight moves to another container while logs are shown
- **THEN** the view switches to the newly highlighted container's logs and the previous stream is closed

### Requirement: Live resource figures
For running containers the desk SHALL show CPU percentage, memory usage and limit, network
receive/transmit rate and block-I/O read/write rate, updated about once per second, and a
short history of CPU and memory so that movement is visible. The container list SHALL show
current CPU and memory for every running container.

#### Scenario: Watching a container
- **WHEN** the stats view is shown for a running container
- **THEN** the figures change as the container's usage changes and a history graph advances about once per second

#### Scenario: Container not running
- **WHEN** the stats view is shown for a container that is not running
- **THEN** it says the container is not running instead of showing stale or zero figures

#### Scenario: Container stops while watched
- **WHEN** a watched container stops
- **THEN** its figures stop updating, the view says it is no longer running, and the desk shows no error

### Requirement: Interactive shell
The operator SHALL be able to open an interactive shell inside a running container. The
program SHALL show the command it will run and let the operator change it before starting.
While the shell runs it SHALL own the whole terminal with a proper terminal session (raw input,
resize propagation). When the shell ends, for any reason, the desk SHALL return exactly as it
was, with the terminal in the desk's full-screen mode and all live streams current.

#### Scenario: Shell and return
- **WHEN** the operator opens a shell in a running container, runs commands and exits the shell
- **THEN** the desk reappears with the same pane focused and the same object highlighted

#### Scenario: Default shell
- **WHEN** the operator accepts the proposed command unchanged
- **THEN** the session runs `bash` if the container has it and `sh` otherwise

#### Scenario: Container not running
- **WHEN** the operator asks for a shell in a container that is not running
- **THEN** the desk says the container is not running and does not leave the workspace

#### Scenario: Shell cannot start
- **WHEN** the container has no such executable or the daemon refuses the exec
- **THEN** the desk returns immediately, shows the daemon's error and remains usable

#### Scenario: Container dies during the session
- **WHEN** the container stops while the shell is open
- **THEN** the session ends, the terminal is restored and the desk reappears

#### Scenario: Terminal resized during the session
- **WHEN** the terminal is resized while the shell is open
- **THEN** the shell inside the container is told the new size, and the desk uses the new size when it returns

#### Scenario: Daemon does not start the process
- **WHEN** the daemon accepts the request for a shell but does not start it within a bounded time
- **THEN** the desk returns with a message saying so, instead of leaving the operator at a blank terminal

#### Scenario: Daemon stops answering during the session
- **WHEN** the session is open and the daemon stops responding without closing the connection
- **THEN** the operator can end the session locally with a documented key sequence, and the desk returns with the terminal restored

#### Scenario: The program inside left terminal modes switched on
- **WHEN** the session ends while the remote program had enabled mouse reporting, bracketed paste, the alternate screen or a hidden cursor
- **THEN** those modes are switched off before the desk returns
