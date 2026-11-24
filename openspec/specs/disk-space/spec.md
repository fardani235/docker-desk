# disk-space Specification

## Purpose
Defines the Space view: where the engine's bytes are, which are shared, what a removal frees,
where each figure comes from, and how the operator gets from a figure to the object.

## Requirements

### Requirement: Opening and closing
`S` SHALL open the Space view from any pane when no dialog is open and no filter is being
typed. `S` or Esc SHALL close it and restore the focus, the kind and the highlight that were
current when it opened. While the view is open, the desk's panes SHALL receive no key.

#### Scenario: Round trip
- **WHEN** the operator presses `S` in the Details pane of Images with `redis:7` highlighted, then Esc
- **THEN** the Details pane of Images is focused and `redis:7` is highlighted

### Requirement: Totals
The view SHALL show, for images, containers, local volumes and build cache, the count, the
active count, the size and the reclaimable size exactly as `GET /system/df` states them,
and SHALL NOT recompute a total the daemon reports. Below API 1.52 the totals SHALL be
labelled as summed by the client library.

#### Scenario: Agreement with the CLI
- **WHEN** `docker system df` and the view read the same engine
- **THEN** every size and reclaimable figure is the same text

### Requirement: Shared and unique bytes
Every image SHALL show its size, its unique size and its shared size as the daemon reports
them. Bytes shared between images SHALL be listed once, against the exact set of images that
hold them. No figure SHALL be presented as freed at once by a removal when it includes bytes
another image keeps.

#### Scenario: A shared base
- **WHEN** two images share their first layer
- **THEN** each shows a unique size smaller than its size, and the first layer's bytes appear once, against both

### Requirement: What a removal frees
For the highlighted image the view SHALL state what removing it alone frees, and what
removing it together with every image sharing its layers frees, with the source of each
figure. When a figure cannot be known exactly it SHALL be stated as a lower bound or as
unknown, with the reason. The remove and prune confirmations SHALL state the bytes they
would free, where that figure comes from and how old it is, without sending a request for
it.

#### Scenario: Alone versus together
- **WHEN** the highlighted image shares a layer with another image
- **THEN** the alone figure is smaller than its size, and the together figure includes the shared layer

#### Scenario: An untag
- **WHEN** the removal only removes one of several references
- **THEN** the confirmation says it frees nothing

### Requirement: Writable layers
Per-container writable-layer sizes SHALL come only from `GET /containers/json?all=1&size=1`,
in a cancellable job the operator starts, and SHALL NOT be requested by any list refresh.

#### Scenario: Not by default
- **WHEN** the view is opened and the lists refresh
- **THEN** no request with `size=1` has been sent

### Requirement: Archive attribution
For an image whose Findings report is cached, the view SHALL attribute the archive's bytes
to its layers and name the largest build instruction when the history maps, without reading
the archive again.

#### Scenario: No second export
- **WHEN** the view shows the attribution of an analysed image
- **THEN** no further `GET /images/get` request has been sent

### Requirement: Provenance and age
Every figure SHALL carry its source (daemon, computed writable layer, computed from layer
sizes, archive) and its age. A figure taken before the last refresh that found the engine
changed — its lists, or the daemon's totals — SHALL be labelled stale; a refresh that finds
nothing changed SHALL NOT make a figure stale.

#### Scenario: A change after the account
- **WHEN** a container is created after the account was taken
- **THEN** the view labels the account stale until it is read again

### Requirement: Going to the object
Enter on an object row SHALL close the view and highlight that object in its own kind with
the List focused.

#### Scenario: A volume
- **WHEN** Enter is pressed on the row of the volume `old-backups`
- **THEN** the Volumes list is shown with `old-backups` highlighted

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
