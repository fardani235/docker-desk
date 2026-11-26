# Spec Delta

## MODIFIED Requirements

### Requirement: One daemon is focused at a time
The panes, the actions, the Events history, the statistics and the logs SHALL all belong to
the focused daemon. The operator SHALL move focus to another connected daemon with a key, and
the screen SHALL show which daemon has focus. One exception: an image copy reads from the
focused daemon and writes to a second connected daemon the operator named; both are shown in
its confirmation and held for the job.

#### Scenario: Moving focus
- **WHEN** the operator moves focus from one daemon to another
- **THEN** the lists, details, Events history and statistics shown are those of the newly
  focused daemon, and nothing from the previous one remains on screen as if it were current

#### Scenario: An action has one target
- **WHEN** the operator starts any action or confirms a selection
- **THEN** every object it acts on belongs to the focused daemon

#### Scenario: A copy names its target
- **WHEN** the operator confirms a copy of an image
- **THEN** the source is the focused daemon's image and the target is the member named in the
  confirmation, not whatever daemon has focus when the job starts
