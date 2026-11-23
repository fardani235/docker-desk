# Spec Delta

## ADDED Requirements

### Requirement: No pointer action mutates
No pointer press or wheel notch SHALL cause a request that changes or deletes anything on the daemon, and none SHALL supply the consent a confirmation waits for. Clicking a menu entry, picker row or dialog row MAY open the same confirmation or form its key opens; the request SHALL still wait for the keyboard.

#### Scenario: Every cell of a confirmation
- **WHEN** a destructive confirmation is open and the operator clicks every cell of the dialog, including its prompt and proceed hint
- **THEN** no request is sent, no job starts, and the dialog stays open

#### Scenario: Menu entry
- **WHEN** the operator clicks `Remove` in an action menu
- **THEN** the same confirmation opens that pressing its key opens, and nothing has yet been sent

#### Scenario: Typed word
- **WHEN** a confirmation requires a typed word and the operator clicks its input or the word itself
- **THEN** the word is not submitted and the plan does not proceed

#### Scenario: Read-only desk
- **WHEN** the desk is read-only and the operator clicks a mutating menu entry
- **THEN** nothing is sent and the status line refuses it exactly as the keyboard would
