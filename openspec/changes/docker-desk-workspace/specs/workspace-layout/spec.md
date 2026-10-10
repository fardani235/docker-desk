## Purpose

Defines the desk itself: the panes that are on screen together, how the operator moves between
them from the keyboard, and how the pane being acted on is made obvious.

## ADDED Requirements

### Requirement: All panes are on screen at once
The workspace SHALL be full-screen and SHALL show these panes simultaneously: Kinds, List,
Details, Events and Jobs, plus a header carrying the daemon identity and a footer listing the
keys that apply to the focused pane.

#### Scenario: Wide terminal
- **WHEN** the terminal is at least 120 columns by 30 rows
- **THEN** all five panes, the header and the footer are visible without overlapping, with the Events and Jobs panes on the opposite side of the screen from the Kinds pane

#### Scenario: Standard terminal
- **WHEN** the terminal is at least 80 columns by 24 rows but narrower than 120 columns
- **THEN** all five panes are still visible in a more compact arrangement

#### Scenario: Terminal too small
- **WHEN** the terminal is smaller than 80 columns by 24 rows
- **THEN** the workspace shows a message stating the minimum size instead of a broken layout, and restores the panes as soon as the terminal is large enough

#### Scenario: Keys while too small
- **WHEN** a key other than quit is pressed while the terminal is too small for the desk to be drawn
- **THEN** it has no effect: nothing is navigated, opened, acted on or confirmed

#### Scenario: Terminal is resized
- **WHEN** the terminal size changes
- **THEN** the panes are laid out again for the new size with selection, scroll positions and focus preserved

### Requirement: The focused pane is unmistakable
Exactly one pane SHALL have focus. The focused pane SHALL be distinguished from the others by
its border and title, in a way that is still distinguishable when colours are disabled.

#### Scenario: Focus moves
- **WHEN** focus moves to another pane
- **THEN** only that pane is drawn with the focused border and title marker and the footer shows that pane's keys

#### Scenario: Colours disabled
- **WHEN** the `NO_COLOR` environment variable is set
- **THEN** the focused pane is still identifiable by a different border style and a marker in its title

### Requirement: Everything is reachable from the keyboard
Every pane, list row, details tab and action SHALL be reachable with the keyboard alone. Focus
SHALL be movable to the next and previous pane and directly to any pane by number. Lists and
scrollable text SHALL support line, page and top/bottom movement with both arrow keys and
vi-style keys.

#### Scenario: Cycling focus
- **WHEN** the operator presses Tab repeatedly
- **THEN** focus visits Kinds, List, Details, Events and Jobs in that order and wraps around; Shift+Tab goes the other way

#### Scenario: Jumping to a pane
- **WHEN** the operator presses a digit 1 to 5
- **THEN** focus moves directly to the corresponding pane

#### Scenario: Moving in a list
- **WHEN** the List pane has focus and the operator presses down/`j`, up/`k`, PageDown, PageUp, `g` or `G`
- **THEN** the highlight moves by one row, by one page, or to the first or last row, and the list scrolls to keep the highlight visible

### Requirement: Help is one key away
The workspace SHALL provide a help overlay listing every key binding, grouped by pane and by
object kind, and SHALL list every action available on the highlighted object in an action menu.

#### Scenario: Opening help
- **WHEN** the operator presses `?`
- **THEN** an overlay lists all key bindings and closes again on Escape or `?`

#### Scenario: Action menu
- **WHEN** the operator presses Enter on a highlighted object in the List pane
- **THEN** a menu lists the actions available for that object with their keys, and choosing one behaves exactly as pressing its key

### Requirement: Quitting restores the terminal
Quitting SHALL restore the terminal to the state it was in before launch, stop all live streams
and leave running jobs' already-issued daemon requests to complete or fail on the daemon side.

#### Scenario: Quit with nothing running
- **WHEN** the operator presses `q` or Ctrl+C with no job running
- **THEN** the program exits with status zero and the terminal shows the shell prompt with normal echo and line editing

#### Scenario: Quit with jobs running
- **WHEN** the operator presses `q` while a job is still running
- **THEN** the program asks for confirmation, naming the running jobs, before exiting
