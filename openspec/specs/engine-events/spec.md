# engine-events Specification

## Purpose
Defines the live view of the engine's own event stream, which is the desk's feedback channel:
the operator acts in one pane and the engine's answer appears in another.

## Requirements

### Requirement: The engine event stream is always on screen
The Events pane SHALL show events reported by the daemon as they happen, newest last, each with
its time, object type, action and the object's name or short ID.

#### Scenario: An action is answered
- **WHEN** the operator confirms stopping a container
- **THEN** the engine's events for that container (for example `kill`, `die`, `stop`) appear in the Events pane without any further key

#### Scenario: Something else changes the engine
- **WHEN** another tool creates a network
- **THEN** a `network create` event appears in the Events pane

### Requirement: Events of interest stand out
Events that indicate failure or destruction (for example `die` with a non-zero exit code, `oom`,
`kill`, `destroy`, `remove`, `delete`) SHALL be visually distinguished from routine events, and
a container `die` event SHALL show the exit code.

#### Scenario: Container crashes
- **WHEN** a container exits with a non-zero exit code
- **THEN** its `die` event is highlighted and shows the exit code

### Requirement: Event history can be read without losing new events
The Events pane SHALL retain a bounded history of recent events (1000 entries). While it follows,
it SHALL show the newest entries. The operator SHALL be able to hold the view: holding SHALL copy
every entry the history holds at that instant into an immutable snapshot, and while held the pane
SHALL draw only that snapshot, so that its rows do not move at any event rate, including a burst
larger than the history. Holding SHALL NOT stop or slow the event subscription: new events SHALL
keep being recorded and SHALL keep refreshing lists, and the pane title SHALL read `‖ held +N`,
N being the number of entries recorded since the snapshot, instead of `● live`; `✗ disconnected`
SHALL take precedence over both.

`Space` and `f` SHALL toggle the hold. A key that reads back (`↑`, `k`, `PgUp`, `Ctrl+B`, `g`,
`Home`) pressed while following SHALL hold first and then move the cursor. Holding SHALL place a
cursor on the newest held entry; every row SHALL have a two-cell gutter, `▸ ` on the cursor row
(in the selection style while the pane is focused) and two spaces otherwise, and there SHALL be
no cursor while following. While held, the movement keys SHALL move the cursor within the
snapshot; `PgDn` and `Ctrl+F` SHALL page and stop at the newest held entry; `↓` past the newest
held entry, `G`, `End`, `Space` and `f` SHALL drop the snapshot and follow again.

#### Scenario: Holding
- **WHEN** the Events pane has focus and the operator presses `Space`
- **THEN** the view stops changing, the cursor is on the newest event and the title reads `‖ held +0`

#### Scenario: Reading back from a live view
- **WHEN** the view follows and the operator presses `↑`
- **THEN** the view is held and the cursor is on the second newest event

#### Scenario: A burst while held
- **WHEN** the view is held and more events arrive than the history holds
- **THEN** the held rows are unchanged, the title counts every new event, and lists keep refreshing

#### Scenario: Resuming
- **WHEN** the view is held and the operator presses `G`, `Space`, or `↓` on the newest held entry
- **THEN** the snapshot is dropped and the view shows the newest events and follows again

#### Scenario: History is bounded
- **WHEN** more events arrive than the history holds
- **THEN** the oldest events are discarded and memory use does not grow beyond the history and one snapshot of it

### Requirement: An event leads to its object
While the Events view is held, `Enter` SHALL go to the object of the event under the cursor, as
the Space view does: focus the List pane, switch to the object's kind, highlight its row, clear
a filter that hides the row and say so. The object SHALL be the event's actor: for `container`
events (exec events included) the container with that ID; for `image` events the row whose
reference equals the event's `name` attribute, otherwise (or for a pull, whose actor is a
reference) the row with that reference, otherwise the first row of the image with that ID; for
`volume` events the volume of that name; for `network` events, `connect` and `disconnect`
included, the network with that ID, never the container named in an attribute. Going to an
object SHALL send nothing but list reads to the daemon and SHALL work in read-only mode.

When the row is not listed, the desk SHALL ask for that list again and wait about two seconds
for the object, selecting it as soon as a refreshed list has it. The wait SHALL be cancelled when
the operator leaves that kind or presses a key in the List pane; when it ends without the
object, the status line SHALL say the object is no longer listed. The hold SHALL stay as it was.

#### Scenario: Enter while following
- **WHEN** the view follows and the operator presses `Enter`
- **THEN** the status line reads "Hold the view first (Space) so the event stays put" and nothing else changes

#### Scenario: Going to a container
- **WHEN** the cursor is on a `container restart` or `exec_start` event and the operator presses `Enter`
- **THEN** the List shows Containers with that container highlighted

#### Scenario: An image with several references
- **WHEN** the cursor is on an image `tag` event whose `name` is one of the image's references
- **THEN** the List shows Images with that reference's row highlighted, not the image's first row

#### Scenario: A network disconnect
- **WHEN** the cursor is on a `network disconnect` event and the operator presses `Enter`
- **THEN** the List shows Networks with the network highlighted

#### Scenario: No object
- **WHEN** the cursor is on a stream marker, or on a `daemon`, `plugin`, `service`, `node`, `secret` or `config` event
- **THEN** the status line says there is no object to go to and the view is unchanged

#### Scenario: An object listed late
- **WHEN** the event's object is newer than the last list refresh
- **THEN** the list is read again and the object is highlighted once it is listed

#### Scenario: An object that is gone
- **WHEN** the event's object is not listed within about two seconds
- **THEN** the status line says it is no longer in the list

#### Scenario: Read-only mode
- **WHEN** the desk is read-only and the operator goes to an event's object
- **THEN** the object is highlighted and no request that changes the engine is sent

### Requirement: Events drive refresh
Engine events SHALL trigger a refresh of the lists they affect, coalesced so that a burst of
events causes a bounded number of refreshes.

#### Scenario: Burst of events
- **WHEN** one hundred container events arrive within one second
- **THEN** the container list is refreshed no more than a handful of times and ends up reflecting the final state

### Requirement: The event stream survives interruptions
If the event stream ends, the program SHALL re-establish it and SHALL record in the Events pane
that the stream was interrupted and resumed.

#### Scenario: Stream interrupted
- **WHEN** the daemon closes the event stream and later accepts connections again
- **THEN** the Events pane shows an interruption marker followed by live events again
