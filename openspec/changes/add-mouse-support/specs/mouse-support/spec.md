# Spec Delta

## Purpose

Defines how the desk accepts pointer input — clicks and the wheel — across its panes, dialogs
and overlays, how that input is enabled and restored, and the limit that it can never mutate
the daemon.

## ADDED Requirements

### Requirement: Mouse reporting is on by default and can be turned off
The desk SHALL enable cell-motion mouse reporting, with SGR coordinates, when it opens. `--no-mouse` SHALL prevent every mouse-enabling sequence and the desk SHALL ignore any mouse event that still arrives, leaving terminal-native selection unmodified. Quitting SHALL switch reporting off before the terminal is returned.

#### Scenario: Default
- **WHEN** the desk opens on a terminal without `--no-mouse`
- **THEN** a click or wheel notch reaches the desk rather than the terminal's scrollback or selection

#### Scenario: Turned off
- **WHEN** the desk is opened with `--no-mouse`
- **THEN** it writes no mouse-enabling sequence, and a mouse report delivered anyway changes nothing

#### Scenario: Quit
- **WHEN** the operator quits the desk
- **THEN** mouse reporting, the alternate screen and the cursor are restored with the rest of the terminal

### Requirement: Reporting survives a shell hand-off
Returning from a container shell SHALL leave pointer input as it was before the hand-off: enabled when the desk started with mouse reporting, absent under `--no-mouse`. The desk SHALL re-enable reporting itself when the terminal is returned.

#### Scenario: Back from a shell
- **WHEN** an interactive shell in a container ends and the desk has redrawn
- **THEN** a click focuses a pane and a wheel notch scrolls again, without restarting the program

#### Scenario: Read-only desk
- **WHEN** the desk is read-only and the shell is therefore unavailable
- **THEN** no hand-off occurs and pointer input is unaffected

### Requirement: A click acts on the cell drawn under it
A left press SHALL act on what the latest frame drew at that cell. It SHALL focus the pane it lands in and SHALL select the kind, list row, Details tab, daemon entry or job drawn there. A click SHALL NOT open an action menu, mark a row, open a file, start a read or send anything.

#### Scenario: Focusing another pane
- **WHEN** the List pane is focused and the operator clicks inside Details
- **THEN** Details is focused, its border and the footer change, and the highlighted row stays as it was

#### Scenario: Picking a kind
- **WHEN** the operator clicks the Volumes line in the Kinds pane
- **THEN** Volumes is the current kind, the List shows volumes, and Kinds is focused

#### Scenario: Highlighting a row
- **WHEN** the operator clicks the third container row
- **THEN** exactly that row is highlighted, the Details pane follows it, and no request is sent

#### Scenario: Switching a tab
- **WHEN** the operator clicks the Stats tab in the Details title
- **THEN** Stats is shown and Details is focused

#### Scenario: Another daemon
- **WHEN** the session holds two daemons and the operator clicks the second entry on the daemon strip
- **THEN** that daemon is focused and every pane is rebuilt from it

#### Scenario: A blank area
- **WHEN** the operator clicks empty space inside the Events pane's rectangle
- **THEN** Events is focused and no cursor or selection moves

#### Scenario: A row is not a command
- **WHEN** the operator clicks a row in the List pane
- **THEN** its action menu does not open and the row is not added to or removed from the selection

### Requirement: The wheel moves the region under the pointer
A wheel notch SHALL move the pane under the pointer without moving keyboard focus. In List, Details, Events and Jobs one notch SHALL travel three rows; in Kinds it SHALL change the kind by one. The List and Jobs highlight SHALL move, Details SHALL scroll its current tab, and Events SHALL hold and move its cursor, resuming live following when a downward notch passes the newest entry.

#### Scenario: Scrolling without taking focus
- **WHEN** the List pane is focused and the pointer is over Details, and the operator turns the wheel
- **THEN** the Details tab scrolls three rows and the List keeps focus

#### Scenario: Reading back in Events
- **WHEN** the Events pane is live and an upward notch arrives over it
- **THEN** the pane is held and its cursor moves three entries back

#### Scenario: Returning to live events
- **WHEN** the Events pane is held and a downward notch is turned while the cursor is on the newest entry
- **THEN** the pane resumes following live events

#### Scenario: Logs
- **WHEN** the Logs tab is following and the pointer over it is wheeled up
- **THEN** following stops and the view scrolls three lines back

#### Scenario: Kinds and Jobs
- **WHEN** the pointer is over Kinds and the wheel turns once
- **THEN** the next kind is chosen, and the pointer over Jobs moves its highlight by three jobs

### Requirement: Choosing in a dialog takes a click, consenting does not
A click on a menu entry SHALL invoke it exactly as Enter does. A click on a picker, daemons or job-viewer row SHALL move its selection, and choosing a picker row SHALL choose it as Enter does. A confirmation SHALL offer no clickable target; only the wheel SHALL move its list.

#### Scenario: Menu entry
- **WHEN** the operator clicks `Stop` in the action menu
- **THEN** the same confirmation opens that pressing `t` opens, and nothing has been sent

#### Scenario: Confirmation
- **WHEN** a confirmation is on screen and the operator clicks anywhere in it
- **THEN** no request is sent, no job starts, and the confirmation stays open

#### Scenario: Picker row
- **WHEN** the operator clicks a context row in the daemon picker
- **THEN** it is selected, and a click on it again chooses it as Enter does

### Requirement: Text rows take focus on click
Clicking a form field, filter, search bar, repository name or export path SHALL give that row keyboard focus and put the caret at the end of its text. A click outside a text row SHALL end the edit and keep its text without submitting a form or asking a registry, and SHALL then act on the clicked cell.

#### Scenario: Form field
- **WHEN** the operator clicks the second row of a create form
- **THEN** that field takes the keyboard, its caret sits at the end of its text, and Enter still submits the form

#### Scenario: Leaving a filter
- **WHEN** a list filter is being typed and the operator clicks a row in the same list
- **THEN** the filter is kept as Enter would keep it, and that row is then highlighted

### Requirement: Files, Space and Repository take the pointer
In the Files, Space and Repository views a click SHALL select the row drawn under it and the wheel SHALL move the list or scrolling text three rows. A click on the filter or repository name line SHALL focus it with the caret at the end. No click SHALL start a read, a measurement or a pull; those remain Enter's.

#### Scenario: A file row
- **WHEN** the operator clicks a row in the Files tree
- **THEN** that row is highlighted and no archive read starts

#### Scenario: A repository tag
- **WHEN** the operator clicks a tag row in the Repository view
- **THEN** that tag is highlighted and no pull or manifest request is sent

#### Scenario: A Space row
- **WHEN** the operator wheels over the Space view's object rows
- **THEN** the highlight moves three rows and the detail below follows it

### Requirement: The pointer does nothing when there is nothing to act on
A mouse event SHALL be ignored when the terminal is too small for the desk, when no daemon is focused, when no frame has been drawn, when it is a motion or release event, or when it is not the left button or the wheel. It SHALL NOT panic, open a dialog, change focus or change a selection.

#### Scenario: Too small
- **WHEN** the terminal is smaller than the desk's minimum and a click arrives
- **THEN** nothing is focused, selected or opened

#### Scenario: No daemon
- **WHEN** no daemon is connected and the operator clicks where a pane would be
- **THEN** nothing happens

#### Scenario: Right button and motion
- **WHEN** the operator right-clicks or moves the pointer with no button pressed
- **THEN** the desk changes nothing

### Requirement: Help and usage name the pointer bindings
The help overlay SHALL list what a click and a wheel notch do and SHALL name `--no-mouse`. The program's `--help` SHALL describe `--no-mouse` and that terminal selection is native without it.

#### Scenario: Help overlay
- **WHEN** the operator presses `?`
- **THEN** the overlay lists the click and wheel bindings and `--no-mouse`

#### Scenario: Usage
- **WHEN** the operator runs `docker-desk --help`
- **THEN** the flags include `--no-mouse` with the reason it exists
