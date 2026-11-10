# Spec Delta

## Purpose

Defines the Files view: what an image's tree is, how a container's own writes are laid over it,
what the view refuses to claim, and how the listing leaves the program.

## ADDED Requirements

### Requirement: Opening and closing the view
The desk SHALL open a full-screen Files view with `F` on a highlighted image or container, and
`Esc` SHALL close it onto the desk exactly as it was. It SHALL open on facts only — identity,
platform, layer count, the daemon's size — and state that nothing has been read. `Enter` SHALL
start one cancellable job that reads the archive and builds the index; no archive request SHALL
be sent before it.

#### Scenario: Opened on an image
- **WHEN** the operator highlights `python:3.12-slim` and presses `F`
- **THEN** the view names the image, its platform and layers, says nothing has been read, and sends no request
- **AND** `Enter` starts one job that reads the archive, shows its bytes and layers as it goes, and draws the tree when it finishes

#### Scenario: Cancelled
- **WHEN** the job is cancelled with `c` or the view is closed while it runs
- **THEN** the stream is closed and no index is kept

#### Scenario: Read-only mode
- **WHEN** the desk is read-only
- **THEN** the view works unchanged and every request it makes is a `GET`

### Requirement: The index is bounded and says what it lost
The index SHALL keep at most 200,000 entries, 4 KiB of each path and 48 MiB of path text,
SHALL count every entry seen after a bound, and SHALL say how many were kept of how many were
seen. The view SHALL NOT state or imply that a path is absent when the index was cut, and the
filter SHALL say that it may miss.

#### Scenario: An image over the bound
- **WHEN** the archive holds 812,431 entries
- **THEN** the header says 200,000 were indexed of 812,431 seen, and the filter says it may miss a match

#### Scenario: A path over the length bound
- **WHEN** an entry's path is longer than 4 KiB
- **THEN** it is not indexed, the header counts it as dropped, and the reason names the length bound

### Requirement: The tree is the merged current filesystem
The tree SHALL be the merged result of the image's layers on the platform asked for: a path
written by several layers appears once with the last writer's size and mode, a path removed by
a whiteout is absent, and an opaque directory hides its lower layers' contents. A layer that
cannot be read SHALL make the tree incomplete: the view SHALL name the layers and SHALL NOT
claim any path is absent.

#### Scenario: Replaced by a later layer
- **WHEN** layer 5 writes `/usr/bin/app` over layer 3's copy
- **THEN** one row appears, with layer 5's size and mode, attributed to layer 5

#### Scenario: Removed by a whiteout
- **WHEN** a later layer removes `/tmp/seed`
- **THEN** no row for it is drawn, and the header reports the count of paths removed by later layers

#### Scenario: An opaque directory
- **WHEN** a layer marks `/var/cache` opaque
- **THEN** only the paths that layer and later layers put under it are shown

#### Scenario: A layer that cannot be read
- **WHEN** a layer is zstd-compressed and the desk cannot decode it
- **THEN** the header names the layers that were not read, and no row or absence is claimed on their account

### Requirement: Entries are shown as their type and never followed
Each entry SHALL be drawn as the type the archive records. A symlink SHALL show its target and
SHALL never be followed; a hard link SHALL appear once, as a link to the path it names; a
socket, FIFO or device node SHALL be typed and never opened. A path SHALL never be resolved,
joined to the host's filesystem or opened.

#### Scenario: A symlink to a directory
- **WHEN** the cursor is on `bin` pointing at `usr/bin`
- **THEN** the target is shown and the row does not expand, and nothing outside the index is read

#### Scenario: A device node
- **WHEN** an entry is a character device
- **THEN** its type is shown and no attempt is made to read, stat or open it

### Requirement: Layer attribution
Each path SHALL name the layer that last wrote it and, where the image's history maps onto its
layers, that layer's build instruction. Where the history does not map, the row SHALL say the
instruction is unknown; where an intervening layer could not be read, the attribution SHALL be
marked unsure with the reason.

#### Scenario: A file from a build step
- **WHEN** the cursor rests on a path introduced by layer 7 of 12
- **THEN** the row and the detail show layer 7 of 12 and the instruction recorded for it

#### Scenario: History shorter than the layers
- **WHEN** the history has fewer entries than the archive has layers
- **THEN** the row says the instruction is unknown and does not guess one

### Requirement: Sizes, and the daemon's different number
A file's size SHALL be the size the archive records for it. A directory's size SHALL be the sum
of the files it holds, computed by the desk, and labelled computed. The header SHALL show the
daemon's size for the image beside the tree's own total, and SHALL state that they measure
different things, because layers overlap.

#### Scenario: A directory's size
- **WHEN** a directory holds files that add up to 412.8 MB
- **THEN** its row says 412.8 MB and the detail says the figure is computed from the files it holds

#### Scenario: The two totals disagree
- **WHEN** the daemon reports 1.2 GB for the image and the tree's files add up to 980 MB
- **THEN** both are shown and the header says they are different measurements, not a contradiction

### Requirement: Movement, expansion and ordering
`Up`/`Down`/`j`/`k`, `PageUp`/`PageDown` and `g`/`G` SHALL move the cursor; `Right` or `Enter`
SHALL expand a directory and `Left` SHALL collapse it or move to its parent; `s` SHALL order by
name or by size. The cursor SHALL follow the path it was on across ordering, reopening and a
rebuilt index.

#### Scenario: Expanding
- **WHEN** the cursor is on a collapsed directory and `Right` is pressed
- **THEN** only its direct children appear, each with its own count and size, and the row stays in place

#### Scenario: Ordering by size
- **WHEN** `s` orders by size
- **THEN** the largest first, files by their own size and directories by their computed size, and the cursor stays on the same path

### Requirement: Filtering by path
`/` SHALL filter by path substring, case-insensitively, showing each match together with the
directories that contain it. The view SHALL count the matches among the entries it holds and
SHALL say when the index was cut. `Esc` SHALL clear the filter and keep the cursor on the path
it was on where that path still exists.

#### Scenario: A deep match
- **WHEN** the filter matches `/usr/local/bin/python3.12`
- **THEN** the row is shown and so are `/usr`, `/usr/local` and `/usr/local/bin`, with the count of matches

#### Scenario: Nothing matches
- **WHEN** no path contains the filter
- **THEN** the view says no path matches, and how many were searched

### Requirement: A container's view is its image's tree with its changes laid over it
On a container the view SHALL mark the daemon's changes over the image's tree: a path the
container added or changed SHALL be marked, a path it deleted SHALL be shown struck through,
and an unchanged path SHALL be shown as the image's. Sizes and layers SHALL come from the
image: a changed file's size SHALL be labelled as the image's, and an added path SHALL have no
size and SHALL not be expandable.

#### Scenario: A file the container added
- **WHEN** the changes list `A /var/lib/app/state.db`
- **THEN** the row is marked added, shows no size, and does not expand

#### Scenario: A file the container changed
- **WHEN** the changes list `C /etc/nginx/nginx.conf`
- **THEN** the row is marked changed, its size is shown as the image's, and the view does not present it as the container's

#### Scenario: A file the container deleted
- **WHEN** the changes list `D /tmp/seed`
- **THEN** the row is drawn struck through, with the image's size and attribution, and marked deleted

#### Scenario: A directory the container created
- **WHEN** the changes list a directory the image does not hold
- **THEN** its row is marked added, cannot be expanded, and says its contents are not read

### Requirement: A container whose image has not been read
When the image's tree has not been built, the view SHALL list the container's changes alone,
with no sizes, and SHALL say that the image's files have not been read and that `Enter` reads
them. The changes SHALL be read when the view opens, without reading the archive.

#### Scenario: Opened on a container
- **WHEN** the operator presses `F` on a container whose image has never been indexed
- **THEN** one changes request is sent, the changed paths appear without sizes, and the header says the image's files have not been read

#### Scenario: After the image is read
- **WHEN** the operator presses `Enter` and the archive job finishes
- **THEN** the image's tree appears with the changes laid over it, without asking the daemon again

#### Scenario: No changes
- **WHEN** the daemon reports no changes
- **THEN** the view says this container has written nothing since its image, once the image's tree is shown

### Requirement: No file contents, ever
The desk SHALL NOT read, show, search or write any file's contents from the index. A name SHALL
be shown and exported with control characters, newlines and invalid UTF-8 escaped, and SHALL
never be treated as a path to open or resolve.

#### Scenario: A hostile name
- **WHEN** an entry's name contains an ANSI escape sequence, a newline and invalid UTF-8
- **THEN** the screen shows the escapes as visible text, on one line, and the export writes the same escaped form

#### Scenario: A traversal-shaped name
- **WHEN** a name is `../../etc/passwd`
- **THEN** it is drawn as a name under the directory the archive places it in, and nothing outside the index is read

### Requirement: The listing leaves through the evidence dialog
`w` SHALL write the listing to one file through the existing evidence dialog, as text or as
JSON, and SHALL refuse with the reason when nothing has been indexed. The file SHALL hold each
path, type, size, mode and layer, and no file's contents.

#### Scenario: Exporting an image's tree
- **WHEN** the index is complete and `w` is confirmed with a path
- **THEN** one file is written, with a row for every indexed path, and none of a file's bytes

#### Scenario: Exporting before reading
- **WHEN** `w` is pressed before the archive has been read
- **THEN** nothing is written and the view says there is no index to write
