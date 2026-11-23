# Spec Delta

## ADDED Requirements

### Requirement: Copying into a container is a mutation
Copying into a container SHALL count as changing it. It SHALL be refused in read-only mode
before any request is sent, and SHALL proceed only from a confirmation that names the daemon,
the container, the absolute local source, the destination path and that files already there are
replaced.

#### Scenario: Confirming
- **WHEN** the operator completes a copy-in form and presses `y` on its confirmation
- **THEN** the copy starts as a job for that container and destination

#### Scenario: Enter is not consent
- **WHEN** the confirmation for a copy-in is shown and the operator presses Enter, `n` or Escape
- **THEN** nothing is sent and no job is recorded

#### Scenario: Read-only
- **WHEN** the desk is in read-only mode
- **THEN** no copy-in form opens, nothing is sent, and the status line says read-only mode

### Requirement: Copying out writes one named local path
Copying out of a container SHALL NOT change the daemon. It SHALL write only the one local path
named in its confirmation, SHALL never replace a path that already exists, and SHALL be allowed
in read-only mode. What it wrote SHALL be removed if the copy is cancelled or fails.

#### Scenario: Read-only copy out
- **WHEN** the desk is in read-only mode and the operator copies a path out
- **THEN** the copy runs, sends only the archive `GET`, and nothing on the daemon changes

#### Scenario: The path exists
- **WHEN** the named local path already exists
- **THEN** nothing is read or written, and the form says the path exists

#### Scenario: Cancelled
- **WHEN** a copy-out is cancelled after it has written part of the destination
- **THEN** those paths are removed and the job says so
