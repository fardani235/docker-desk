# container-attention Specification

## Purpose

Defines the Attention view: how the desk decides which containers need a person, how it reads
every connected daemon for one screen, how it orders and counts them, how the operator reaches
a container from it, and what it refuses to claim.

## Requirements

### Requirement: One screen answers for every connected daemon

The desk SHALL open a full-screen Attention view with `H` showing one block per connected
daemon of the session, the focused daemon first. Each block SHALL name its endpoint, engine
version and when its containers were read. A disconnected member SHALL appear with the reason
and no container rows. The view SHALL change nothing and SHALL work in read-only mode.

#### Scenario: Opening with several daemons
- **WHEN** the operator presses `H` in a session with three connected daemons
- **THEN** the view opens with the focused daemon's block first and the other two following, and no request has changed anything

#### Scenario: A disconnected member
- **WHEN** a member of the session is disconnected
- **THEN** its block names the endpoint and the reason, and shows no container rows

#### Scenario: Read-only
- **WHEN** the desk is in read-only mode and the operator presses `H`
- **THEN** the view opens and sends only reads

### Requirement: Every container falls into one class

Every container SHALL be placed in exactly one class: **needs you** — restarting, unhealthy,
dead, or exited with a non-zero code; **watch** — running with three or more restarts, or
created and never started; **fine** — running and healthy; **unknown** — no healthcheck, or a
check still starting; **not a problem** — paused, removing, or exited with code 0.

#### Scenario: A restart loop
- **WHEN** a container's state is restarting
- **THEN** it is needs you

#### Scenario: A failing probe
- **WHEN** a running container's health is unhealthy
- **THEN** it is needs you

#### Scenario: A failed exit
- **WHEN** a container has exited with a code other than 0
- **THEN** it is needs you

#### Scenario: A clean exit
- **WHEN** a container has exited with code 0, or is paused
- **THEN** it is not a problem and is shown only as a count

#### Scenario: No healthcheck
- **WHEN** a running container has no healthcheck configured
- **THEN** it is unknown, never fine

### Requirement: A failed exit is shown with its reason

For a container that needs you because it exited, the desk SHALL read and show its exit code,
whether it was OOM-killed and when it finished, and SHALL name an OOM kill explicitly. Failed
exits SHALL be ordered newest first.

#### Scenario: Out of memory
- **WHEN** a container exited because the kernel killed it for memory
- **THEN** its row says it was OOM-killed and shows the exit code and finish time

#### Scenario: Several failures
- **WHEN** three containers exited non-zero at different times
- **THEN** the most recent failure is listed first

### Requirement: A class that needs a detail waits for it

When a container's class depends on a detail not yet read — an exit code, an OOM verdict or a
restart count — the view SHALL show it as being read, SHALL NOT count it as fine or as needing
you until the answer arrives, and SHALL settle it as soon as the answer does.

#### Scenario: An exited row while its code is read
- **WHEN** a container has exited and its code has not been read yet
- **THEN** its row says it is being read and is not counted in either class

#### Scenario: The answer arrives
- **WHEN** the read answers with a non-zero code
- **THEN** the row becomes needs you at once and is counted

### Requirement: The view reads on open and on request, and follows events

On open the view SHALL use the focused daemon's current list at once and read every other
connected member's containers once. It SHALL read again when the operator presses `r`. While
open, it SHALL re-read a daemon only when that daemon's event stream reports a container exit,
OOM or health change, and SHALL NOT poll. Each block SHALL show its read time; a failed read
SHALL be shown in that block.

#### Scenario: The focused daemon is instant
- **WHEN** the view opens
- **THEN** the focused daemon's block is filled from the list already held, before any other read answers

#### Scenario: A background daemon changes
- **WHEN** a container on an unfocused daemon exits or becomes unhealthy while the view is open
- **THEN** that daemon's block is read again and its read time advances

#### Scenario: Nothing happened
- **WHEN** the view stays open and no event arrives
- **THEN** no list request is made until `r` is pressed

#### Scenario: A read fails
- **WHEN** a member's container list cannot be read
- **THEN** its block says why and keeps the rest of the view usable

### Requirement: Reasons are read progressively and bounded

The view SHALL read a container's details only when its class needs them — exit code, OOM
kill, finish time, restart count — SHALL fill rows as answers arrive, and SHALL stop at a
named bound, saying how many rows were not inspected. Closing the view or cancelling SHALL
stop further reads.

#### Scenario: Details fill in
- **WHEN** the view needs exit codes for six containers
- **THEN** rows appear as each answer arrives, and the view remains usable throughout

#### Scenario: The bound is reached
- **WHEN** more candidates need details than the view will read
- **THEN** it stops at the bound and says how many were not inspected

### Requirement: Ordering and counts

Within each block, needs-you rows SHALL precede watch rows. Needs-you rows SHALL be ordered
restarting, unhealthy, dead, then exited (newest first). Fine, unknown and not-a-problem
containers SHALL appear only as counts. The view SHALL show the container total and the count
that needs you.

#### Scenario: Mixed problems
- **WHEN** a daemon has one restarting container, one unhealthy container and one dead container
- **THEN** they are listed in that order, before any watch row

#### Scenario: Counts
- **WHEN** a daemon has twelve containers, two of them needing a person
- **THEN** the block shows twelve containers, two needing attention, and the other classes as counts

### Requirement: Enter goes to the container, Esc goes back

Enter on a row SHALL close the view, focus that row's daemon, wait for the container to appear
in its list and select it. If the daemon is disconnected or the container is gone, the desk
SHALL say so and not move. Esc or `H` SHALL close the view and restore the pane, kind and
highlight that were current when it opened.

#### Scenario: Jump to another daemon
- **WHEN** the operator presses Enter on a container of a daemon that is not focused
- **THEN** that daemon becomes focused, its Containers list is shown with the container selected, and the view is closed

#### Scenario: The container is gone
- **WHEN** Enter is pressed for a container that no longer exists
- **THEN** the desk says so and leaves the focus and selection as they were

#### Scenario: Closing
- **WHEN** the operator presses Esc or `H`
- **THEN** the pane, kind and highlight that were current before the view opened are restored

### Requirement: An empty answer is a clear answer

When no container needs you and none is watch, the view SHALL say so with the totals and the
time each daemon was read, and SHALL NOT show an empty table or a spinner.

#### Scenario: Nothing wrong
- **WHEN** every container is fine, unknown or not a problem
- **THEN** the view says nothing needs attention and gives the totals per daemon

### Requirement: Attention is visible without the view

The Containers list SHALL show a container's health in its state cell whenever the daemon
reports one, and SHALL warn when it is not healthy. The desk SHALL show a count of containers
that need you by the signals the list already carries — dead, restarting and unhealthy — and
SHALL say that the view may find more by reading exit codes and restarts.

#### Scenario: An unhealthy row
- **WHEN** a running container's health is unhealthy
- **THEN** its state cell says so and is drawn in the warning colour

#### Scenario: The count
- **WHEN** two containers are restarting and one is unhealthy
- **THEN** the desk shows that three need attention and that the view opens the full list

#### Scenario: No healthcheck is not healthy
- **WHEN** a running container has no healthcheck
- **THEN** its state cell does not call it healthy

### Requirement: The view is read-only and claims only what it read

The view SHALL offer no action that changes a daemon, SHALL send no request other than the
container lists, container details and events it already follows, and SHALL NOT show images,
disk, compose drift or other signals in this version. It SHALL NOT claim a container is fine
when its health is unknown.

#### Scenario: No actions
- **WHEN** the operator presses a mutating key in the view
- **THEN** nothing is sent and the view stays usable

#### Scenario: Out of scope
- **WHEN** the view is open
- **THEN** it shows no image, disk, compose or registry information
