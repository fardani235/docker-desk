# Spec Delta

## MODIFIED Requirements

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

## ADDED Requirements

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
