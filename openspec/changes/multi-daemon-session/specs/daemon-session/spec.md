# Spec Delta

## Purpose

Defines the set of Docker daemons connected in one running session: which one has focus,
how daemons are added and removed while the desk is open, what happens to the daemons that
are not focused, and what the operator sees when no daemon is connected.

## ADDED Requirements

### Requirement: The session holds several daemons
The program SHALL hold a set of connected daemons for the whole session. The daemon chosen at
launch is the first member. Further daemons SHALL be added from the picker, and any member
SHALL be removed while the program is open.

#### Scenario: A second daemon is added while running
- **WHEN** the operator adds a daemon from the picker while the desk is open
- **THEN** the daemon joins the session without restarting the program, and the previously
  connected daemons keep their state

#### Scenario: The launch daemon is removed
- **WHEN** the operator removes the daemon chosen at launch
- **THEN** it leaves the session like any other member

### Requirement: One daemon is focused at a time
The panes, the actions, the Events history, the statistics and the logs SHALL all belong to
the focused daemon. The operator SHALL move focus to another connected daemon with a key, and
the screen SHALL show which daemon has focus.

#### Scenario: Moving focus
- **WHEN** the operator moves focus from one daemon to another
- **THEN** the lists, details, Events history and statistics shown are those of the newly
  focused daemon, and nothing from the previous one remains on screen as if it were current

#### Scenario: An action has one target
- **WHEN** the operator starts any action or confirms a selection
- **THEN** every object it acts on belongs to the focused daemon

### Requirement: The picker lists the Docker contexts on this machine
The picker SHALL list the contexts stored in the Docker configuration, with their endpoint and
how each is reached. Each daemon SHALL appear once in the session: adding an endpoint that is
already connected SHALL NOT create a second member.

#### Scenario: Adding a context that is already connected
- **WHEN** the operator picks a context whose endpoint is already a member
- **THEN** the session keeps one member for that endpoint and says so

#### Scenario: Context whose stored endpoint cannot be read
- **WHEN** a context in the store has no usable endpoint
- **THEN** the picker lists it as unusable with the reason, and it cannot be added

### Requirement: A daemon added while down is shown and retried
A daemon added from the picker MAY be unreachable at the time. The session SHALL keep it as a
member shown as disconnected with the reason, SHALL keep retrying it, and SHALL keep the other
members working.

#### Scenario: Added daemon does not answer
- **WHEN** the operator adds a daemon that does not answer
- **THEN** the picker and the daemon's entry show it as disconnected with the reason, and the
  other daemons remain usable

#### Scenario: Added daemon comes back
- **WHEN** a disconnected daemon answers again
- **THEN** its entry shows it as connected and its live updates resume without a restart

### Requirement: Unfocused daemons keep an event stream and a ping
A connected daemon that is not focused SHALL keep one event stream and a periodic ping. It
SHALL NOT keep statistics, log or list refreshes running for itself while it is unfocused.

#### Scenario: Background daemon goes quiet
- **WHEN** the connection to an unfocused daemon is lost
- **THEN** the status line says so with the daemon's name, and the entry shows it disconnected

#### Scenario: Focusing a background daemon
- **WHEN** the operator focuses a connected daemon that was not focused
- **THEN** its lists are requested at once, the panes show a loading state until they answer, and the previous daemon's data is never shown as current

### Requirement: A daemon added from the picker never asks on the terminal
A daemon added while the desk is open SHALL NOT ask the operator anything on the terminal. When
its ssh login would need a passphrase, a password or a host-key confirmation, the daemon SHALL
stay disconnected and say why, and the rest of the session SHALL keep working.

#### Scenario: Added ssh daemon needs a passphrase
- **WHEN** the operator adds an `ssh://` daemon whose key has a passphrase
- **THEN** nothing is asked on the terminal, the daemon is shown disconnected with ssh's reason, and the desk continues

### Requirement: Background events are notices, not history
An event from an unfocused daemon that matters to the operator — a container exiting, or a
daemon disconnecting or reconnecting — SHALL appear as a notice in the status line naming the
daemon. It SHALL NOT be added to the Events history of the focused daemon.

#### Scenario: A container exits on a background daemon
- **WHEN** a container stops on an unfocused daemon
- **THEN** the status line names that daemon and the container, and the Events pane of the
  focused daemon is unchanged

### Requirement: Removing a daemon
The operator SHALL be able to remove any member. A daemon with a running job SHALL NOT be
removed: the picker SHALL list those jobs and the removal SHALL be refused until they finish or
are cancelled. Removing a daemon SHALL end its streams and its connection, and SHALL end any
ssh process the program started for it.

#### Scenario: Removal refused while work runs
- **WHEN** the operator removes a daemon that has a running job
- **THEN** the removal is refused, the picker names the job, and the daemon stays connected

#### Scenario: Removal of an idle daemon
- **WHEN** the operator removes a daemon with no running job
- **THEN** its streams stop, its connection closes, and no ssh process the program started for it remains

#### Scenario: Removing the focused daemon
- **WHEN** the focused daemon is removed and other members are connected
- **THEN** focus moves to one of the connected members and the operator is told which

### Requirement: The desk stays open with no daemon connected
When the last member is removed, the program SHALL stay open on an empty state that says no
daemon is connected and offers the picker. In that state no action on a daemon SHALL be
possible, and quitting SHALL still work.

#### Scenario: Last daemon removed
- **WHEN** the last member is removed
- **THEN** the desk shows the empty state with the picker one key away, and it does not exit

#### Scenario: Keys in the empty state
- **WHEN** an action key other than the picker or quit is pressed in the empty state
- **THEN** nothing is acted on and the status line says that no daemon is connected
