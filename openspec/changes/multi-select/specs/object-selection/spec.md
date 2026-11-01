## Purpose

Defines selecting several objects of one kind in the List pane, and acting on them as one
plan: which actions apply, how objects that cannot be included are reported, how the plan is
confirmed and rendered, and how it is executed.

## ADDED Requirements

### Requirement: Selecting rows
In the List pane, `Space` SHALL toggle the highlighted row in or out of the current kind's
selection without moving the highlight, and `a` SHALL add every row the current filter shows.
Containers, images, volumes and networks SHALL be selectable; compose projects SHALL NOT, and
`Space` or `a` on them SHALL say so. Each kind SHALL have its own selection. `Space` SHALL keep
paging in the Details and Events panes. Selecting SHALL send nothing to the daemon and SHALL be
allowed in read-only mode.

#### Scenario: Marking
- **WHEN** the operator presses `Space` on two container rows
- **THEN** both rows show a marker in the two-cell gutter and the pane title reads `2 Containers (12) - 2 selected`

#### Scenario: Unmarking
- **WHEN** the operator presses `Space` on a marked row
- **THEN** the row leaves the selection

#### Scenario: Select what the filter shows
- **WHEN** the filter is `shop-` and the operator presses `a`
- **THEN** exactly the rows matching the filter are selected

#### Scenario: Space elsewhere
- **WHEN** the Details or Events pane is focused and the operator presses `Space`
- **THEN** that pane pages down and the selection is unchanged

### Requirement: Selection follows object identity
A selection SHALL be keyed by object identity (container ID, image ID and reference, volume
name, network ID). It SHALL survive refreshes and reordering, refresh each marked object's
displayed data, and drop an object that no longer exists once its list has loaded. Marked rows
hidden by the filter SHALL remain selected.

#### Scenario: Refresh
- **WHEN** containers are added and a marked container changes state
- **THEN** the same objects remain selected

#### Scenario: Vanished object
- **WHEN** a marked container is removed outside the desk
- **THEN** it leaves the selection and the title count drops

### Requirement: Esc steps back one thing at a time
In the List pane `Esc` SHALL clear the selection if there is one; otherwise clear the filter if
there is one; otherwise move focus to the Kinds pane.

#### Scenario: Ladder
- **WHEN** there is a selection and a filter and the operator presses `Esc` three times
- **THEN** the first clears the selection, the second the filter, the third focuses Kinds

### Requirement: Actions apply to the whole selection
With a non-empty selection, start, stop, restart, pause, unpause, kill and remove on containers
and remove on images, volumes and networks SHALL apply to every selected object of that kind,
not to the highlighted row. The action menu opened on a selection SHALL offer only actions
that apply to it and SHALL name the selection in its title. Read-only mode SHALL refuse every
set action, sending nothing.

#### Scenario: Stop with a selection
- **WHEN** three containers are selected, another is highlighted, and the operator presses `t`
- **THEN** the confirmation concerns the three selected containers and not the highlighted one

#### Scenario: Read-only
- **WHEN** the desk is read-only, two containers are selected and the operator presses `t` or `d`
- **THEN** no dialog opens, no request is sent, and the status line says read-only mode

### Requirement: Single-object actions refuse a selection
With a non-empty selection, shell, connect and disconnect SHALL refuse with a status message
giving the reason and SHALL send nothing. Prune SHALL refuse likewise. Pull and create SHALL be
unaffected.

#### Scenario: Shell with a selection
- **WHEN** two containers are selected and the operator presses `e`
- **THEN** no form opens, no terminal hand-off happens, no request is sent, and the status line says shell works on one object at a time

### Requirement: A set plan is built from the single-object rules
A set plan SHALL be computed from one fresh snapshot by applying the single-object constructor
to each selected object. Its steps SHALL be exactly the steps of the members that constructor
allows, in selection order, each once. A member it finds not applicable or would refuse (in
use, running, predefined, vanished) SHALL be excluded and recorded with that reason in
`Plan.Skipped`; it SHALL NOT abort the set. The plan SHALL be `Listed`, and its `TotalSize`
SHALL be the sum of its steps' sizes.

#### Scenario: Exactly the runnable objects
- **WHEN** two running containers and one exited container are selected and stop is chosen
- **THEN** the plan holds exactly the two stop steps and names the exited container under Skipped with "is not running (exited)"

#### Scenario: In-use object
- **WHEN** a volume in use and an unused volume are selected and remove is chosen
- **THEN** the plan removes only the unused volume and the dialog lists the other under "Not included" with "In use by 1 container: …"

#### Scenario: Nothing runnable
- **WHEN** every selected object is excluded
- **THEN** the dialog says there is nothing to do, lists every object with its reason, and offers nothing to confirm

### Requirement: Selecting every reference of an image deletes it
When a selection holds every reference of an image with several, the set plan SHALL remove the
references one by one, the last removal carrying the image's size and an in-use re-check, so
that the image is deleted. If the image is in use, every one of those rows SHALL be excluded
with the users named.

#### Scenario: Both tags selected
- **WHEN** both references of an unused image are selected and remove is confirmed
- **THEN** both requests succeed and the image no longer exists

### Requirement: A set confirmation types the count
A set confirmation SHALL be typed, whatever the action, and the word SHALL be the verb followed
by the number of listed requests (`stop 3`, `remove 7`); the prompt SHALL read "Type <word> to
confirm". Only the exact word followed by Enter SHALL proceed; `y`, the bare verb or another
count SHALL be refused with "Nothing was sent". Single-object confirmations SHALL keep their
wording and tiers.

#### Scenario: Count required
- **WHEN** the set confirmation asks for `stop 2` and the operator types `stop` or `stop 3` and presses Enter
- **THEN** nothing is sent and the dialog stays open saying so

### Requirement: Executing a set
Consenting to a set SHALL start one job that executes exactly the plan's steps through the
unchanged executor: one request each, never forced, continuing after a failure and reporting
how many of how many failed. The selection SHALL be cleared when the job starts and kept when
the confirmation is declined.

#### Scenario: Partial failure
- **WHEN** a set of three removals runs and the daemon refuses one
- **THEN** the other two are sent and succeed, the job ends failed with "1 of 3 failed", and its output shows each success and the daemon's message for the failure

### Requirement: A set confirmation fits the smallest terminal
At 80×24, with any endpoint, the set confirmation SHALL keep what is acted on, the daemon, the
prompt and the input on screen; the listed objects and the not-included objects SHALL scroll,
and every one of them SHALL be reachable.

#### Scenario: Long set, long remote endpoint
- **WHEN** 32 volumes are selected on a long `ssh://` endpoint at 80×24 and remove is chosen
- **THEN** the identity, every daemon line, "Type remove 28 to confirm" and the input are on screen, and scrolling reaches every listed and not-included volume
