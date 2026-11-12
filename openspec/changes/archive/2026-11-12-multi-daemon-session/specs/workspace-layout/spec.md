# Spec Delta

## ADDED Requirements

### Requirement: The daemon strip is on screen at every supported size
The workspace SHALL show a daemon strip listing every member of the session, marking the focused
one and showing each one's state (connected, disconnected, or read-only). The strip SHALL be
visible at the standard size of 80 columns by 24 rows, and it SHALL NOT remove any of the five
panes to make room.

#### Scenario: Strip at the standard size
- **WHEN** the terminal is 80 columns by 24 rows with three members in the session
- **THEN** all five panes and the strip are visible without overlapping, and the focused member is marked

#### Scenario: Many members
- **WHEN** the session holds more members than fit in the strip
- **THEN** the strip keeps the focused member visible and shows that others are hidden

#### Scenario: Strip is reached from the keyboard
- **WHEN** the operator presses the key that moves focus to another member
- **THEN** focus moves through the members in order and the strip shows the change
