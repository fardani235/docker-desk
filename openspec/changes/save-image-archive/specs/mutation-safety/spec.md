# Spec Delta

## ADDED Requirements

### Requirement: Saving an image archive writes one named local path
Saving an image archive SHALL write exactly the one local path named in its form, SHALL never
replace a path that already exists, and SHALL never create a folder. It SHALL send only the
archive `GET`, change nothing on the daemon, and be allowed in read-only mode. What it wrote
SHALL be removed if the save is cancelled or fails.

#### Scenario: The path exists
- **WHEN** the named path is already taken
- **THEN** nothing is read or written, and the form says the desk never replaces a file

#### Scenario: Cancelled
- **WHEN** the save is cancelled after it has written part of the archive
- **THEN** the partial file is removed and the job says nothing remains

#### Scenario: Read-only
- **WHEN** the desk is in read-only mode
- **THEN** the save runs, sends only the archive `GET`, and nothing on the daemon changes

#### Scenario: No other request
- **WHEN** the archive is saved
- **THEN** the only request sent for it is the image archive read; no mutation and no other endpoint is called
