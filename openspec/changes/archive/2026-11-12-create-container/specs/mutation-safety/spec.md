# Spec Delta

## ADDED Requirements

### Requirement: Creating something is confirmed by its form
A creation SHALL NOT be sent by any single key press. The form describing the new object SHALL
be its confirmation: it SHALL name the daemon, restate the complete configuration, state any
download it will perform first, and state what will be created or started. Nothing SHALL be
sent until `Enter` on a form that has been on screen long enough to read; every other key
SHALL send nothing.

#### Scenario: The key that opens the form
- **WHEN** the operator presses the key that opens a create form
- **THEN** the form is shown and no request that changes the daemon has been sent

#### Scenario: Enter typed ahead
- **WHEN** the create key and `Enter` arrive in quick succession, before the form is displayed
- **THEN** nothing is sent and the form stays open

#### Scenario: Form not drawn
- **WHEN** a create form is open but the terminal is too small for the desk to be drawn
- **THEN** no key submits it, and it is shown again, still unsubmitted, when the terminal is
  large enough

#### Scenario: Declining
- **WHEN** the operator presses Escape in the create form
- **THEN** the form closes, nothing is sent to the daemon and nothing is recorded as a job
