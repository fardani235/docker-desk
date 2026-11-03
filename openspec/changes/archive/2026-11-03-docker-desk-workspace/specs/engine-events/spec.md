## Purpose

Defines the live view of the engine's own event stream, which is the desk's feedback channel:
the operator acts in one pane and the engine's answer appears in another.

## ADDED Requirements

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
The Events pane SHALL retain a bounded history of recent events. The operator SHALL be able to
scroll back through it; while scrolled back, new events SHALL keep being recorded and the pane
SHALL indicate that newer events exist; returning to the end SHALL resume following.

#### Scenario: Scrolling back
- **WHEN** the Events pane has focus and the operator scrolls up
- **THEN** the view stops following, shows older events, and indicates that it is not at the end

#### Scenario: Resuming
- **WHEN** the operator presses `G` in the Events pane
- **THEN** the view jumps to the newest event and follows again

#### Scenario: History is bounded
- **WHEN** more events arrive than the history holds
- **THEN** the oldest events are discarded and memory use does not grow

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
