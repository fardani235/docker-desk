# Spec Delta

## ADDED Requirements

### Requirement: Copying an image to another daemon is a typed, two-ended action
An image copy SHALL require the operator to type the source tag, or the short image ID for an
untagged row, and press Enter, after a confirmation naming both daemons, the exact object, the
target effect and whether a target tag will be created or moved. No other input SHALL send
anything. Read-only mode SHALL refuse it before any request is sent to either daemon.

#### Scenario: Typed confirmation
- **WHEN** the copy confirmation is armed and the operator types the tag and presses Enter
- **THEN** the copy starts, and nothing else until then sent an image request

#### Scenario: `y` is not consent
- **WHEN** the operator presses `y` or Enter without the word
- **THEN** nothing is sent and the dialog stays open

#### Scenario: Read-only
- **WHEN** the desk is in read-only mode
- **THEN** no destination chooser opens, nothing is sent to either daemon, and the status line says read-only mode

#### Scenario: An existing target tag moves
- **WHEN** the load moves a target tag that named another image
- **THEN** the confirmation named that image's short ID first, and the job reports the moved tag as the daemon did

#### Scenario: The source is not a target
- **WHEN** a copy is confirmed
- **THEN** the only write is the image load on the chosen target; the source is only read
