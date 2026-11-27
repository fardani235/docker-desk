# terminal-safety Specification

## Purpose

Defines what the desk owes the terminal when it draws text it did not choose: how an untrusted
value is escaped at the drawing boundary, what the frame guard still removes as the last
defence, which values must keep their raw bytes because they are sent back, and how the screen
and the exports are kept to one mapping.

## Requirements

### Requirement: Untrusted text is escaped where it is drawn

Every string the desk draws that it did not compose — a daemon's container name, image,
command, working directory, labels, mount paths or error prose, a compose project's name, an
archive's path, or operator input drawn back — SHALL be escaped at the point it becomes a drawn
line. Every character in it that could act on the terminal SHALL be drawn as visible text, and
no byte of the value SHALL be dropped.

#### Scenario: A label carrying a colour sequence

- **WHEN** a highlighted container has the label `note` whose value contains a conceal sequence
- **THEN** Details › Info draws the sequence as the text `\x1b[8m`, and the raw frame contains no conceal sequence

#### Scenario: A newline in a mount path

- **WHEN** a container's mount destination contains a newline
- **THEN** the pane draws `\n` in its place, and the number of lines the pane draws does not change

#### Scenario: A confirmation restates the object

- **WHEN** a confirmation for removing that container is open
- **THEN** its body draws the escaped name, and the frame contains no sequence the desk's styles did not emit

#### Scenario: Colour disabled

- **WHEN** the desk runs with colour disabled and a daemon's value carries a colour sequence
- **THEN** the sequence is drawn as text and does not change the terminal's colours

#### Scenario: Operator input drawn back

- **WHEN** the operator types a local path containing an escape sequence into the copy-in form
- **THEN** the form and its confirmation draw the escape as text

### Requirement: The frame guard stays the last defence, unchanged

Every line of every frame SHALL still pass the frame guard before it is written to the terminal.
The guard SHALL remove every sequence that takes no cell: cursor movement, erase, OSC, DCS, APC,
PM, SOS, device queries, control characters, a newline inside a line, C1 controls and bytes that
are not UTF-8. It SHALL keep the colour sequences the desk's own styles emit.

#### Scenario: A line the desk composed badly

- **WHEN** a line the desk built carries an OSC sequence that sets the window title
- **THEN** the frame written to the terminal contains no OSC sequence

#### Scenario: The desk's own styling survives

- **WHEN** the desk draws any styled line of its own
- **THEN** its colour sequences appear unchanged in the frame

#### Scenario: An escaped line needs no repair

- **WHEN** a line has been escaped at the drawing boundary and is passed to the guard
- **THEN** the guard returns it unchanged

### Requirement: Escaping is display-only

Escaping SHALL change only what is drawn. Values the desk sends to the daemon or compares with
what the daemon reported SHALL keep their raw bytes: a rename sends the name the operator typed,
a copy sends the path as given, and a compose project is matched by its real label value. A
confirmation that requires typing SHALL compare against the text it shows.

#### Scenario: A path with a backslash is copied in

- **WHEN** the operator copies in to `/srv/a\b`
- **THEN** the request carries one backslash, not two

#### Scenario: A project named by a label

- **WHEN** a compose project's name label contains a control character
- **THEN** stopping the project finds its containers by the label's real value

#### Scenario: A rename uses what was typed

- **WHEN** the operator renames a container to a name containing no escape
- **THEN** the request carries exactly the bytes that were typed

### Requirement: Screen and export use one mapping

The escaping used for a drawn value SHALL be the mapping the desk's exports already use, so that
a name drawn in a view and written to a file are the same string, and every byte of the original
value is accounted for in what is shown or written.

#### Scenario: A name in a view and in a file

- **WHEN** an export names an object whose name carries an escape sequence
- **THEN** the file contains the same escaped text the screen shows

#### Scenario: Every byte is accounted for

- **WHEN** a value contains a byte that is not valid UTF-8
- **THEN** the drawn text names that byte, and the value is not silently shortened
