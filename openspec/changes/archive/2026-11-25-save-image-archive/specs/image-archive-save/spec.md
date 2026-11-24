# Spec Delta

## Purpose

Defines saving one highlighted image's archive to a file on this machine: the action and form,
the image and platform saved, the streaming write and its path rules, progress, cancellation
and cleanup.

## ADDED Requirements

### Requirement: The archive action is on the highlighted image
Images SHALL offer `w save archive…` for the highlighted row, in the action menu and as the
direct key from the List pane. It SHALL act on one image only, SHALL ask for nothing until the
form is submitted, and SHALL work in read-only mode because saving is a read.

#### Scenario: The key
- **WHEN** an image is highlighted and the operator presses `w`
- **THEN** the save form opens and no archive request has been sent

#### Scenario: Several selected
- **WHEN** a selection exists and the operator presses `w`
- **THEN** the desk says the action works on one image at a time, and nothing is sent

#### Scenario: Read-only
- **WHEN** the desk is read-only and the operator saves an image
- **THEN** the form opens and the stream is a `GET`; nothing on the daemon changes

### Requirement: The form names what will be written
Before anything is read, the form SHALL name the image, its short ID, the reference or ID that
will be saved, the platform, the local path, and that the daemon is only read. The path SHALL
be prefilled as `~/docker-desk-image-<object>-<YYYYMMDD>.tar`, with `~/` expanded only on
submission. A folder that does not exist, a directory, or a path already taken SHALL be
refused in the form, and no folder SHALL be created.

#### Scenario: Opened
- **WHEN** the form opens on `nginx:1.27`
- **THEN** it shows the tag, the short ID, the platform, the prefilled path and that nothing on the daemon changes

#### Scenario: Path already taken
- **WHEN** the operator enters a path where a file exists and presses Enter
- **THEN** the form says the desk never replaces a file, the form stays open and nothing is read

#### Scenario: Missing folder
- **WHEN** the path names a folder that does not exist
- **THEN** the form says the folder must exist, and no archive request is sent

### Requirement: The archive is the shown image and platform, with its tag
For a tagged row the desk SHALL re-inspect the reference immediately before saving, SHALL
refuse to write if it no longer names the image that was shown, and SHALL save by the
reference so the archive carries the tag. An untagged row SHALL be saved by its image ID. The
platform SHALL be the one the image is inspected as, and SHALL be named in the form and the
job.

#### Scenario: Tagged
- **WHEN** the operator saves `nginx:1.27`
- **THEN** the daemon is asked for that reference's archive for the inspected platform, and the archive carries the tag

#### Scenario: Tag moved
- **WHEN** the reference now names another image by the time the save starts
- **THEN** nothing is written and the job says the tag moved

#### Scenario: Untagged
- **WHEN** the highlighted row has no reference
- **THEN** the image ID is saved, and the form says the archive will be untagged

#### Scenario: Platform
- **WHEN** the image is inspected as `linux/arm64/v8`
- **THEN** the form and the job name that platform, and the save asks for it

#### Scenario: The highlight moves
- **WHEN** the form is open for one image and the highlight moves to another
- **THEN** Enter still saves the image the form named, or reports that it no longer exists; it never saves the other

### Requirement: The file is written atomically and can be cancelled
The archive SHALL stream to a hidden temporary file beside the destination, SHALL be created
mode 0600, and SHALL reach the chosen name only when complete, without replacing anything. The
job SHALL report bytes written; cancelling SHALL close the daemon stream and remove the
temporary file; a failure SHALL leave no file under the destination's name and SHALL report the
reason.

#### Scenario: Success
- **WHEN** the stream ends normally
- **THEN** the file appears at the chosen path with mode 0600, and the job reports its bytes, path and platform

#### Scenario: Cancelled
- **WHEN** the operator cancels the job
- **THEN** the stream is closed, the temporary file is removed, and no file appears at the chosen path

#### Scenario: Destination taken while writing
- **WHEN** another file appears at the destination during the write
- **THEN** the desk refuses to replace it, removes its temporary file, and the job says so

#### Scenario: Daemon or disk failure
- **WHEN** the daemon stream or the local write fails
- **THEN** the job fails with the reason, the temporary file is removed and nothing is left at the chosen path
