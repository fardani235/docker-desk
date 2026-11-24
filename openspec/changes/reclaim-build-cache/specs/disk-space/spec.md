# Spec Delta

## MODIFIED Requirements

### Requirement: Reclaim
The view SHALL offer reclaim only through a confirmation. Removing an object or pruning
images, volumes or networks SHALL use the desk's existing preview, which deletes exactly the
listed objects. Build cache SHALL be reclaimed through `b`, a fresh account's preview and the
daemon's own build-cache prune. No path SHALL force a removal. In read-only mode the view
SHALL show every figure and no reclaim entry point.

#### Scenario: Read-only
- **WHEN** the desk is read-only
- **THEN** the view shows its totals and figures, its hints offer none of `d`, `X` or `b`, and those keys open nothing

#### Scenario: Object removal and prune
- **WHEN** the operator presses `d` or `X` on an object row
- **THEN** the existing exact-list confirmation opens for that object or kind, and no build-cache request is involved

## ADDED Requirements

### Requirement: Build cache has its own reclaim action
With the Space view open, `b` SHALL open the build-cache confirmation without requiring a
highlighted row, provided no dialog is open. In read-only mode the key SHALL not be offered
and SHALL open nothing.

#### Scenario: The key
- **WHEN** the Space view is open and the operator presses `b`
- **THEN** the desk starts reading the daemon's account and opens the build-cache confirmation when it answers, sending nothing that changes the daemon

#### Scenario: Read-only
- **WHEN** the desk is read-only
- **THEN** the hints omit `b` and pressing it opens nothing

#### Scenario: While a dialog is open
- **WHEN** a dialog is already open and `b` is pressed
- **THEN** the key is the dialog's, and no build-cache confirmation opens behind it

### Requirement: The preview comes from a fresh account and names the scope
Opening the confirmation SHALL read `GET /system/df` first and list every candidate record
with its type, description, size and in-use/shared flag, the candidate count and the account's
age. An in-use record SHALL never be a candidate. The default scope SHALL be unused records
whose type is not `internal` or `frontend`; `Tab` SHALL switch to every unused record
including those types, read a fresh account for it, and clear any typed word.

#### Scenario: Default scope
- **WHEN** the confirmation opens with the default scope
- **THEN** it lists the unused, non-internal, non-frontend records and says how many in-use records are kept

#### Scenario: All types
- **WHEN** the operator presses Tab
- **THEN** the scope becomes all unused records including internal and frontend ones, the account is read again, and the typed word is empty

#### Scenario: In use
- **WHEN** a cache record is in use at the account
- **THEN** it is not listed as a candidate and cannot be sent

#### Scenario: Nothing to prune
- **WHEN** every record is in use or there are no records
- **THEN** the dialog says there is nothing to prune and offers nothing to confirm

#### Scenario: Shared bytes
- **WHEN** candidate records are marked shared
- **THEN** their sizes are shown but no sum is presented as bytes that will certainly be freed

### Requirement: Confirming sends one build-cache prune and reports the daemon's answer
Confirmation SHALL require typing `prune` and pressing Enter. It SHALL send one
`POST /build/prune` with the chosen scope and no force, then log the daemon's deleted records
and reclaimed bytes, name any deleted record that was not in the preview, and mark the
account stale. A daemon refusal SHALL fail the job with the daemon's message.

#### Scenario: The word
- **WHEN** the confirmation is armed and the operator types `prune` and presses Enter
- **THEN** one build-cache prune is sent and nothing else

#### Scenario: Wrong word
- **WHEN** the operator types anything else and presses Enter
- **THEN** nothing is sent and the dialog says the text does not match

#### Scenario: The report
- **WHEN** the daemon answers with the records it deleted and the bytes it reclaimed
- **THEN** the job logs those figures, and the Space account is marked stale

#### Scenario: A record outside the preview
- **WHEN** the daemon's report names a record that the account did not list
- **THEN** the job says the daemon also removed it, rather than claiming the preview was exact

#### Scenario: Refused
- **WHEN** the daemon refuses the prune
- **THEN** the job fails with the daemon's message and the account is read again
