# Spec Delta

## ADDED Requirements

### Requirement: Each member has its own read-only mode
Every member SHALL be either read-write or read-only, independently of the others. A read-only
member SHALL refuse every action that changes or deletes something, and the shell, before any
request for it is sent, while its reads behave as usual. A member added while the desk is open
SHALL start in the session's default mode — read-only when `--read-only` is in force, otherwise
the mode stored for it, or read-write when there is none.

#### Scenario: A mixed session
- **WHEN** one member is read-only and another is read-write
- **THEN** a mutating action on the read-only member is refused with nothing sent, and the same kind of action on the read-write member opens its confirmation as usual

#### Scenario: A member added later
- **WHEN** a daemon is added from the picker while the desk is open
- **THEN** it starts in the mode the session gives it, and changing the mode of another member does not change it

#### Scenario: Toggling the mode of a member with a running job
- **WHEN** the operator tries to change the mode of a member that has a running job
- **THEN** the change is refused, the picker names the job, and the member keeps its mode

### Requirement: Read-only applies to the member, not the session
Toggling a member's mode SHALL take effect before the next request the desk sends to that
member and SHALL send nothing itself. A request already on its way SHALL not be recalled.
Toggling SHALL not disconnect the member, restart its streams or change its data.

#### Scenario: Turning read-only on
- **WHEN** the operator turns a read-write member read-only and then triggers a mutating action on it
- **THEN** nothing is sent to that daemon, and the desk says the member is read-only

#### Scenario: Turning read-only off
- **WHEN** the operator turns a read-only member read-write and then triggers a mutating action on it
- **THEN** the action's ordinary confirmation opens and the other members' modes are unchanged

#### Scenario: Streams survive
- **WHEN** a member's mode is toggled
- **THEN** its event stream, lists and connection continue without a reconnect

### Requirement: A member's mode is visible wherever the member is
The strip SHALL mark every read-only member. The header SHALL state the focused member's mode
and SHALL NOT drop it at any supported terminal size, as it does not drop the endpoint. The
picker SHALL state each member's mode in words. The help SHALL say how the mode is changed.

#### Scenario: Read-only member in the strip
- **WHEN** the session has a read-only member that is not focused
- **THEN** its entry in the strip is marked read-only

#### Scenario: Focus moves
- **WHEN** focus moves from a read-only member to a read-write one
- **THEN** the header no longer says the focused member is read-only

#### Scenario: Narrow terminal
- **WHEN** the terminal is at its minimum width
- **THEN** the focused member's read-only state is still on screen

### Requirement: The picker changes a member's mode
The picker SHALL offer a key that toggles the selected member between read-only and read-write.
It SHALL refuse the change while `--read-only` is in force, saying that the whole session is
locked read-only, and SHALL refuse it while the member has a running job. A completed toggle
SHALL be reported in the status line and SHALL be saved.

#### Scenario: Toggling in the picker
- **WHEN** the operator selects a member and presses the toggle key
- **THEN** the member's mode changes, the strip and header show it, the status line says so and nothing is sent to any daemon

#### Scenario: Toggle under --read-only
- **WHEN** the program was started with `--read-only` and the operator presses the toggle key
- **THEN** nothing changes and the picker says the session is locked read-only by that flag

#### Scenario: Toggle while a job runs
- **WHEN** the selected member has a running job and the operator presses the toggle key
- **THEN** nothing changes and the picker names the job

### Requirement: A stored session rejoins the launch
When a state file is in use, its stored members SHALL be added after the launch daemon has
answered and the workspace has opened. The launch daemon SHALL remain the one the existing
precedence chooses and SHALL be the first member; a stored entry for it contributes only its
stored mode. Restored members SHALL be background members that never ask anything on the
terminal; one that cannot be resolved or reached SHALL be skipped or shown disconnected,
without interrupting the launch.

#### Scenario: The previous session had three daemons
- **WHEN** the desk opens with a state file naming the launch daemon and two others
- **THEN** the launch daemon is focused first and the two others join the session in the background

#### Scenario: The launch daemon's stored mode
- **WHEN** the launch daemon is in the state file as read-only
- **THEN** it is read-only for this run unless the stored flag cannot be trusted, and the other members keep their own modes

#### Scenario: A stored member cannot be reached
- **WHEN** a stored member does not answer at launch
- **THEN** it is shown disconnected with the reason and retried, and the other members keep working

#### Scenario: A restored ssh member needs an answer
- **WHEN** a restored `ssh://` member would need a passphrase, a password or a host-key confirmation
- **THEN** nothing is asked on the terminal, the member is shown disconnected with the reason, and the desk opens

### Requirement: The stored focus is restored when it connects
The program SHALL remember which member had focus. Once that member is connected, the desk
SHALL move focus to it and SHALL say so in the status line. Until then, and when it never
connects, focus SHALL remain on the launch daemon and the stored member SHALL keep retrying.
Focus restored this way SHALL follow the ordinary rule that nothing of one daemon is shown as
another's.

#### Scenario: The stored focused member connects
- **WHEN** the member that had focus when the desk was last closed answers after launch
- **THEN** focus moves to it and the status line says which daemon the desk moved to

#### Scenario: The stored focused member never answers
- **WHEN** the member that had focus does not answer for the whole run
- **THEN** the desk stays on the launch daemon and the stored member remains a disconnected member being retried

#### Scenario: The stored focus is the launch daemon
- **WHEN** the member that had focus is the launch daemon
- **THEN** nothing moves and no notice is shown
