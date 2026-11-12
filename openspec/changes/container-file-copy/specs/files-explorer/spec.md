# Spec Delta

## ADDED Requirements

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

## MODIFIED Requirements

### Requirement: No file contents, ever
The desk SHALL NOT read, show, search or write any file's contents from the index: nothing a
file holds is drawn, searched, exported, logged or kept, and an export holds names and metadata
only. A copy the operator asks for is the only thing that reads contents, and it streams a
container's archive to the destination they named without keeping or drawing it. A name SHALL
be shown and exported with control characters, newlines and invalid UTF-8 escaped, and SHALL
never be treated as a path to open or resolve.

#### Scenario: A hostile name
- **WHEN** an entry's name contains an ANSI escape sequence, a newline and invalid UTF-8
- **THEN** the screen shows the escapes as visible text, on one line, and the export writes the same escaped form

#### Scenario: A traversal-shaped name
- **WHEN** a name is `../../etc/passwd`
- **THEN** it is drawn as a name under the directory the archive places it in, and nothing outside the index is read

#### Scenario: A copy is asked for
- **WHEN** the operator copies a path out
- **THEN** the container's archive is read, the index and the tree are unchanged, and no contents are drawn or written to the job's output
