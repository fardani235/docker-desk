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
The view SHALL offer reclaim only through the existing single-object removal and prune
preview. It SHALL NOT send a mutating request itself, call a prune endpoint or force. In
read-only mode it SHALL show every figure and no reclaim entry point.

#### Scenario: Read-only
- **WHEN** the desk is read-only
- **THEN** the view shows its totals and figures, its hints offer neither `d` nor `X`, and those keys open nothing
