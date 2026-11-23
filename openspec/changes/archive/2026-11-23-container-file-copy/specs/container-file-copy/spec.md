# Spec Delta

## Purpose

Defines copying a file or directory between this machine and a container from the Files view:
the two forms, the exact-name destination rule, recursive contents, modes and links, the
streamed archive in both directions, what is refused on this machine, what is skipped, what is
cleaned up, and how it behaves in read-only mode.

## ADDED Requirements

### Requirement: Copying starts from the Files view on a container
The Files view SHALL offer copy out (`o`) and copy in (`i`) for the highlighted path, and both
forms SHALL start with that path filled in. On an image the keys SHALL say copies are between a
container and this machine. Opening a form SHALL send nothing.

#### Scenario: Copying a path out
- **WHEN** the cursor is on `/etc/nginx/nginx.conf` in a container's Files view and the operator presses `o`
- **THEN** the copy-out form opens with that container path filled in and no request has been sent

#### Scenario: Copying in to a path
- **WHEN** the operator presses `i` with the cursor on `/etc/nginx`
- **THEN** the copy-in form opens with that container path as the destination

#### Scenario: An image
- **WHEN** the Files view is on an image and the operator presses `i` or `o`
- **THEN** the status line says copies are between a container and this machine, and no form opens

### Requirement: The destination is the path named
In both directions the destination SHALL be the exact path the operator gives: the file or
directory is created there, or files with the same names there are replaced. The source SHALL
NOT be nested inside an existing destination directory. The destination's parent directory SHALL
already exist, and this rule SHALL be stated in both forms.

#### Scenario: A file to a file
- **WHEN** the operator copies a local `a.conf` to `/etc/app/a.conf`
- **THEN** the container has a file at `/etc/app/a.conf`, not `/etc/app/a.conf/a.conf`

#### Scenario: A directory into a directory
- **WHEN** the operator copies a local directory whose contents include `conf.d` to `/etc/app`
- **THEN** `/etc/app/conf.d` is created or replaced, and files at `/etc/app` that the source does not name are left alone

#### Scenario: A missing parent
- **WHEN** the destination's parent directory does not exist
- **THEN** nothing is copied, and the desk shows the daemon's refusal rather than creating it

### Requirement: Copying out reads the container's archive as one job
Copying out SHALL stream the daemon's archive for the highlighted path into one local
destination, as one cancellable job, and SHALL NOT change the daemon. A directory SHALL be
copied with everything under it. File modes SHALL be preserved, symlinks SHALL be recreated and
never followed, and the job SHALL report the entries and bytes it wrote.

#### Scenario: A file
- **WHEN** the operator copies `/var/lib/app/state.db` out to `/tmp/state.db`
- **THEN** a job streams the archive and writes `/tmp/state.db` with the container's mode

#### Scenario: A directory
- **WHEN** the operator copies `/etc/nginx` out to `/tmp/nginx-copy`
- **THEN** `/tmp/nginx-copy` is a new directory holding the same tree, and nothing outside it is written

#### Scenario: A symlink
- **WHEN** the highlighted path is a symlink
- **THEN** a symlink is created at the local destination, pointing where the container's pointed, and the target is not copied

#### Scenario: Nothing is on this machine to overwrite
- **WHEN** the copy finishes
- **THEN** no path other than the one named has been created, changed or removed

### Requirement: Copying out refuses what exists
The copy-out form SHALL refuse a local destination that already exists, of any type, and SHALL
refuse a destination whose parent directory does not exist. Nothing SHALL be read from the
daemon when either refusal applies.

#### Scenario: The destination exists
- **WHEN** the operator names `/tmp/nginx` and a file or directory is already there
- **THEN** nothing is read or written, and the form says the path already exists

#### Scenario: The parent is missing
- **WHEN** the operator names `/tmp/not-here/nginx` and `/tmp/not-here` does not exist
- **THEN** nothing is read or written, and the form says the parent must exist

#### Scenario: A relative path
- **WHEN** the operator enters `nginx.conf`
- **THEN** the form shows the absolute path it will write, and `~` is expanded

### Requirement: A failed or cancelled copy-out leaves nothing behind
When a copy-out fails or is cancelled, every path it created SHALL be removed, and the job
SHALL say what was removed. A directory the copy created SHALL not remain.

#### Scenario: Cancelled while streaming
- **WHEN** the operator cancels a copy-out part-way through
- **THEN** the partial file or directory is removed, and the job is shown as cancelled

#### Scenario: The daemon refuses the path
- **WHEN** the path no longer exists in the container
- **THEN** nothing remains on this machine and the job fails with the daemon's message

#### Scenario: A local write fails
- **WHEN** the disk is full or a path cannot be written
- **THEN** what was written is removed, and the job reports the local error

### Requirement: Copying in streams a local path into the container
Copying in SHALL send a local file or directory to the destination as one job, with a directory
copied with everything under it. Modes SHALL be preserved, symlinks SHALL be recreated and never
followed, and the source SHALL be streamed rather than held whole in memory. The job SHALL report
the entries and bytes it sent.

#### Scenario: A file
- **WHEN** the operator copies `/etc/hosts` to `/tmp/hosts`
- **THEN** the container has a file at `/tmp/hosts` with the local mode

#### Scenario: A directory
- **WHEN** the operator copies a local `assets` directory to `/srv/assets`
- **THEN** every file and directory beneath it appears beneath `/srv/assets`

#### Scenario: A path that is already there
- **WHEN** the destination already holds files the source names
- **THEN** those files are replaced, and other files at the destination are left alone

#### Scenario: A source that does not exist
- **WHEN** the local path does not exist
- **THEN** no form confirmation is offered, and the form says the source does not exist

### Requirement: Copying in can replace files, so it is confirmed
The copy-in confirmation SHALL show the container and daemon, the absolute local source, its
entry count and total bytes, the container destination, and that files already at the
destination are replaced. The copy SHALL be completed only by `y` on that confirmation; `Enter`,
`n` or Escape SHALL send nothing. Read-only mode SHALL refuse the copy before a form opens.

#### Scenario: The confirmation
- **WHEN** the operator submits a copy-in form for a directory of 12 files and 4.2 MB
- **THEN** the confirmation names the container, the source, the destination, the 12 files and 4.2 MB, and that files there are replaced

#### Scenario: Enter is not consent
- **WHEN** the confirmation is shown and the operator presses Enter or `n`
- **THEN** nothing is sent and no job is recorded

#### Scenario: Read-only
- **WHEN** the desk is in read-only mode and the operator presses `i`
- **THEN** no form opens, nothing is sent, and the status line says read-only mode

### Requirement: Archive entries cannot escape
In both directions no entry SHALL be able to read or write outside the destination: an absolute
name, a `..` component or a symlink followed during extraction SHALL be refused. Symlinks SHALL
be created as links, never followed. An entry that is not a regular file, a directory or a
symlink SHALL be skipped and named in the job's output.

#### Scenario: A traversal-shaped name
- **WHEN** a container's archive holds an entry named `../../etc/passwd`
- **THEN** it is not written outside the destination, the copy fails or is refused, and nothing outside was touched

#### Scenario: An absolute name
- **WHEN** an entry is named `/etc/passwd`
- **THEN** it is treated according to the entry rules and never written at that absolute path

#### Scenario: A device or FIFO
- **WHEN** the source tree holds a character device or a FIFO
- **THEN** it is not opened or recreated, and the job lists it as skipped

#### Scenario: A symlink to a file outside the tree
- **WHEN** the source holds a symlink pointing at `/etc/passwd`
- **THEN** the link is copied as a link and its target's contents are not read

### Requirement: The view follows a copy-in
After a successful copy-in the desk SHALL read the container's change list again, so the copied
paths appear as added or changed. It SHALL NOT rebuild the image's tree.

#### Scenario: A new file appears
- **WHEN** a copy-in creates `/srv/assets/app.js` in a container whose image never had it
- **THEN** the row appears marked added after the copy, without another archive read

#### Scenario: A replaced file
- **WHEN** a copy-in replaces a file the image has
- **THEN** the row is marked changed and its size is still the image's

### Requirement: Copy jobs name what they did and never their contents
A copy job's title, target and output SHALL name the container, the source and the destination,
the entries and bytes handled, and the paths it skipped. They SHALL NOT contain any file's
contents.

#### Scenario: Reading the job
- **WHEN** the operator opens a finished copy job
- **THEN** it lists the source, destination, what was written or skipped, and no file data

#### Scenario: Read-only copy out
- **WHEN** the desk is read-only and a copy-out runs
- **THEN** it works, sends only the archive `GET`, and its job says nothing was changed on the daemon
