# Spec Delta

## MODIFIED Requirements

### Requirement: A prune shows exactly what it will destroy and destroys only that
For images, volumes and networks, before a prune is confirmed the dialog SHALL list every
individual object it will delete, by name and short ID, with the total count (and total size
for images). After confirmation the program SHALL delete exactly the listed objects — each
one individually, without force — and nothing that was not listed, even if more objects
became eligible in the meantime. The build cache is pruned under its own rule below.

#### Scenario: Preview
- **WHEN** the operator asks to prune images
- **THEN** the dialog lists each image that would be removed and the total size, and the list can be scrolled if it does not fit

#### Scenario: Nothing to prune
- **WHEN** nothing is eligible
- **THEN** the dialog says there is nothing to prune and offers nothing to confirm

#### Scenario: New eligible object appears after the preview
- **WHEN** another unused volume is created after the preview was shown and the operator then confirms
- **THEN** only the volumes that were listed are deleted and the new one is left alone

#### Scenario: Listed object became in use
- **WHEN** a listed object is in use by the time its deletion is attempted
- **THEN** it is not deleted, the daemon's refusal is recorded for it, and the remaining listed objects are still processed

#### Scenario: Listed tagged image became in use
- **WHEN** a tagged image listed by an "all unused" prune is used by a container by the time its references are about to be removed
- **THEN** none of its references is removed, although the daemon itself would allow an untag

#### Scenario: Prune eligibility is conservative
- **WHEN** a network or volume is referenced only by stopped containers
- **THEN** it is treated as in use and is not offered for pruning

## ADDED Requirements

### Requirement: Build-cache pruning names its candidates, not an exact set
Build-cache reclaim SHALL require typing `prune`, list the candidate records from a fresh
account, never offer an in-use record, and state that the daemon re-evaluates eligibility: it
may remove records created or released after the account and may keep one now in use. The job
SHALL claim only the daemon's report and name any deletion that was not a candidate, and no
force SHALL be sent.

#### Scenario: Preview
- **WHEN** the operator opens the build-cache confirmation
- **THEN** the dialog lists each candidate record with its type, description and size, says the daemon decides the final set, and no request that changes the daemon has been sent

#### Scenario: New record appears after the preview
- **WHEN** a cache record becomes unused after the account was taken and the operator then confirms
- **THEN** the daemon may remove it as well, and the job names it as one that was not in the preview

#### Scenario: A candidate became in use
- **WHEN** a listed candidate is in use by the time the prune is sent
- **THEN** the daemon may keep it, and the job reports fewer records than the preview listed

#### Scenario: The claim is the daemon's
- **WHEN** the prune finishes
- **THEN** the deleted records and reclaimed bytes reported are the daemon's, never a sum computed by the desk

#### Scenario: Read-only
- **WHEN** the desk is read-only
- **THEN** the build-cache action is not offered and nothing is sent
