# Spec Delta

## MODIFIED Requirements

### Requirement: The engine event stream is always on screen
The Events pane SHALL show events reported by the focused daemon as they happen, newest last, each
with its time, object type, action and the object's name or short ID. Events from unfocused
daemons SHALL NOT enter this history; the daemon-session capability reports the ones that matter
as notices.

#### Scenario: An action is answered
- **WHEN** the operator confirms stopping a container
- **THEN** the engine's events for that container (for example `kill`, `die`, `stop`) appear in the Events pane without any further key

#### Scenario: Something else changes the engine
- **WHEN** another tool creates a network on the focused daemon
- **THEN** a `network create` event appears in the Events pane

#### Scenario: Focus moves to another daemon
- **WHEN** the operator focuses another connected daemon
- **THEN** the Events pane shows that daemon's event history, and the previous daemon's events are not shown

#### Scenario: Event on an unfocused daemon
- **WHEN** a `network create` event occurs on an unfocused daemon
- **THEN** the Events pane of the focused daemon is unchanged
