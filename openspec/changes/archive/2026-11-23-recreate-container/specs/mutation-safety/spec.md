# Spec Delta

## ADDED Requirements

### Requirement: Re-creating a container is confirmed by typing its word
A plan that stops a running container, renames it and creates another in its place SHALL
require the operator to type `recreate` and press Enter. It SHALL be armed as every other
confirmation is: a key arriving before it has been on screen long enough is ignored. `y`,
Enter without the word, and every other key SHALL send nothing; Escape SHALL close it.

#### Scenario: The word matches
- **WHEN** the confirmation is armed and the operator types `recreate` and presses Enter
- **THEN** the plan starts

#### Scenario: The word does not match
- **WHEN** the operator types anything else and presses Enter
- **THEN** nothing is sent and the dialog says the text does not match

#### Scenario: `y` is not consent
- **WHEN** the confirmation is shown and the operator presses `y` or Enter without typing the word
- **THEN** nothing is sent and the dialog stays open

#### Scenario: Typed ahead
- **WHEN** the action key and `recreate` arrive before the confirmation is displayed
- **THEN** nothing is sent and the confirmation stays open

#### Scenario: Declining
- **WHEN** the operator presses Escape
- **THEN** the confirmation closes, nothing is sent and nothing is recorded as a job

## MODIFIED Requirements

### Requirement: Creating something is confirmed by its form
A creation entered field by field SHALL NOT be sent by any single key press. The form
describing the new object SHALL be its confirmation: it SHALL name the daemon, restate the
complete configuration, state any download it will perform first, and state what will be
created or started. Nothing SHALL be sent until `Enter` on a form that has been on screen long
enough to read; every other key SHALL send nothing. A re-creation of an existing container is
confirmed by its plan's typed dialog instead.

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
