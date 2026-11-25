# files-explorer Specification

## Purpose
Defines the Files view: what an image's tree is, how a container's own writes are laid over it,
what the view refuses to claim, and how the listing leaves the program.

## Requirements

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
them — except on a regular file, where `Enter` opens that file's reader. The changes SHALL be
read when the view opens, without reading the archive.

#### Scenario: Opened on a container
- **WHEN** the operator presses `F` on a container whose image has never been indexed
- **THEN** one changes request is sent, the changed paths appear without sizes, and the header says the image's files have not been read

#### Scenario: After the image is read
- **WHEN** the operator presses `Enter` on a path that is not a regular file and the archive job finishes
- **THEN** the image's tree appears with the changes laid over it, without asking the daemon again

#### Scenario: No changes
- **WHEN** the daemon reports no changes
- **THEN** the view says this container has written nothing since its image, once the image's tree is shown

#### Scenario: A changed file
- **WHEN** the tree is not built, the changes list a regular file, and the operator presses `Enter` on it
- **THEN** that file's reader opens from the container's current filesystem, and the image archive is not read

### Requirement: No file contents, ever
The index, tree, filter and exports SHALL hold names and metadata only. Contents SHALL be read
only for the reader, on request, for one regular file in a container; what it reads is drawn
there and never written, exported, searched, logged or kept after it closes. A copy still reads
contents only when asked and draws none. Names SHALL be shown and exported with controls,
newlines and invalid UTF-8 escaped, never as a path to open.

#### Scenario: A hostile name
- **WHEN** an entry's name contains an ANSI escape sequence, a newline and invalid UTF-8
- **THEN** the screen shows the escapes as visible text, on one line, and the export writes the same escaped form

#### Scenario: A traversal-shaped name
- **WHEN** a name is `../../etc/passwd`
- **THEN** it is drawn as a name under the directory the archive places it in, and nothing outside the index is read

#### Scenario: A copy is asked for
- **WHEN** the operator copies a path out
- **THEN** the container's archive is read, the index and the tree are unchanged, and no contents are drawn or written to the job's output

#### Scenario: The reader asks
- **WHEN** the operator opens a regular file in a container
- **THEN** one archive request is sent for that path, its contents appear only in the reader, and the index, the filter and every export still hold names and metadata only

#### Scenario: Images are not read
- **WHEN** the highlighted file belongs to an image's tree
- **THEN** no archive is requested for its contents, and the reader says image files are not read

#### Scenario: Closed
- **WHEN** the reader closes
- **THEN** its contents are discarded and nothing was written anywhere

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

### Requirement: The view offers a copy on a container
On a container the Files view SHALL offer copy out and copy in for the highlighted path, and its
footer and help SHALL list both. On an image the keys SHALL say copies are between a container
and this machine. Opening a copy form SHALL send nothing and SHALL leave the tree as it is.

#### Scenario: The keys are offered
- **WHEN** the Files view is on a container
- **THEN** the footer and the help list copy out and copy in for the highlighted path

#### Scenario: An image
- **WHEN** the view is on an image and one of the keys is pressed
- **THEN** the desk says copies are between a container and this machine, and no form opens

#### Scenario: Opening a form changes nothing
- **WHEN** a copy form is opened
- **THEN** no request has been sent, the tree is unchanged and no size appears for a path the view did not have one for

### Requirement: A regular file in a container can be opened
`Enter` on a regular file in a container's tree SHALL open the reader, sending one
`GET /containers/{id}/archive` for that path with progress and cancel; no request SHALL be made
before Enter. An image's file, a directory, symlink, hard link, device, FIFO, socket, a deleted
path, or an entry whose type is unknown SHALL be refused with its type, never followed or
opened. The read SHALL work in read-only mode.

#### Scenario: A regular file
- **WHEN** the operator presses `Enter` on a regular file in a container
- **THEN** one archive request is sent for exactly that path, and the reader shows the file's metadata and contents

#### Scenario: No tree yet
- **WHEN** the image's tree has not been built and `Enter` is pressed on an added regular file
- **THEN** the file is read from the container, and no image archive is requested

#### Scenario: An image
- **WHEN** `Enter` is pressed on a file in an image's tree that has already been read
- **THEN** the reader says image files are not read, and no request is sent

#### Scenario: Not a regular file
- **WHEN** `Enter` is pressed on a symlink, hard link, device, FIFO or socket
- **THEN** the reader says what it is and does not follow or open it

#### Scenario: Deleted
- **WHEN** `Enter` is pressed on a path the container deleted
- **THEN** the reader says it was deleted and sends no archive request

#### Scenario: Read-only
- **WHEN** the desk is read-only
- **THEN** the reader works and its only request is the archive `GET`

### Requirement: The reader bounds and classifies what it reads
The reader SHALL keep at most 1 MiB, 10,000 lines and 2 KiB per line, and SHALL say what was
cut. It SHALL show contents only for valid UTF-8 text without NUL bytes; otherwise it SHALL
show metadata only, with no contents and no hex view. Controls, ANSI escapes and invalid bytes
SHALL be escaped.

#### Scenario: Text
- **WHEN** a 40-line shell script is read
- **THEN** every line is shown with controls escaped

#### Scenario: A large file
- **WHEN** the file is 3 MiB
- **THEN** the first 1 MiB is shown, and the reader says exactly that the rest was cut

#### Scenario: Long lines and many lines
- **WHEN** a line is 3 KiB or the file has 20,000 lines
- **THEN** the line is cut at 2 KiB or the reader stops at 10,000 lines, and says which bound was reached

#### Scenario: Binary
- **WHEN** the bytes are not valid UTF-8 or contain NUL
- **THEN** the reader shows metadata only, says it is not text, and draws no bytes

### Requirement: The reader presents the file and keeps the tree behind it untouched
The reader SHALL name the path, container, type, size, mode, owner, time and source, and for a
changed path say the tree's size is the image's. It SHALL consume every key and wheel notch
while open; keys scroll or close, and none reach the tree. It SHALL NOT search, export or write.

#### Scenario: Header
- **WHEN** a file is shown
- **THEN** the reader names the path, container, size, mode, owner, time and that the source is the container's current filesystem

#### Scenario: Changed path
- **WHEN** the path is one the container changed
- **THEN** the reader shows the container's current contents and says the tree's size is the image's

#### Scenario: Behind it
- **WHEN** the reader is open and a key or wheel notch arrives
- **THEN** it scrolls or closes the reader, and nothing reaches the tree behind it

#### Scenario: No way out to a file
- **WHEN** the reader is open
- **THEN** there is no export or search action, and nothing is written anywhere

### Requirement: The reader closes back to the tree and reports failures
`Esc` or `q` SHALL close the reader onto the Files view exactly as it was; its contents SHALL
be discarded. A missing path, a daemon refusal, an answer that is not the requested regular
file, a cancelled read or a vanished container SHALL be reported with no content and no claim;
the stream SHALL be closed.

#### Scenario: Closing
- **WHEN** the operator presses Esc or `q` in the reader
- **THEN** the Files view is shown with the same cursor, expansion, order and filter, and no content remains

#### Scenario: Missing now
- **WHEN** the path has gone by the time the reader asks
- **THEN** the reader reports that it is gone, and no content is kept

#### Scenario: The wrong entry
- **WHEN** the daemon answers with a directory, a link or a different name
- **THEN** the reader shows no contents and says what it received

#### Scenario: Cancelled
- **WHEN** the read is cancelled
- **THEN** the stream is closed, the reader reports the cancellation and nothing is kept

#### Scenario: Daemon failure
- **WHEN** the archive request fails
- **THEN** the reader shows the daemon's message and no contents
