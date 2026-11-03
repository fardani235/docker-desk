## Purpose

Defines the desk's colour themes: which palettes exist, how one is chosen and switched, and
the limits on what a theme may change, so that no theme can make the desk less safe or less
legible than another.

## ADDED Requirements

### Requirement: Built-in themes
The program SHALL provide these themes: `dark`, `light`, `ansi`, `dracula`, `nord`, `gruvbox`,
`solarized-dark`, `solarized-light`, `catppuccin` and `mono`. `dark` SHALL be the palette the
desk had before themes existed. `ansi` SHALL use only the terminal's own sixteen colours.
`mono` SHALL use no colour.

#### Scenario: Listing
- **WHEN** the operator runs the program with `--help`
- **THEN** every theme name is listed with a one-line description

#### Scenario: The original palette
- **WHEN** the theme is `dark`
- **THEN** the desk is drawn with exactly the colours it had before this change

### Requirement: Choosing a theme
The theme SHALL be chosen by the `--theme` flag, or else by the `DOCKER_DESK_THEME` environment
variable, or else automatically: `dark` when the terminal's background is dark or unknown,
`light` when it is light. The name `auto` SHALL mean the automatic choice. Names SHALL be
matched without regard to case. An unknown name SHALL be refused at launch with a message
listing the known names, without opening the workspace.

#### Scenario: Flag
- **WHEN** the program is started with `--theme nord`
- **THEN** the desk is drawn in the Nord palette

#### Scenario: Environment
- **WHEN** `DOCKER_DESK_THEME=light` is set and no `--theme` is given
- **THEN** the desk is drawn in the light palette

#### Scenario: Flag over environment
- **WHEN** both `--theme` and `DOCKER_DESK_THEME` are given
- **THEN** the flag's theme is used

#### Scenario: No setting
- **WHEN** neither is given and the terminal's background is dark or cannot be determined
- **THEN** the desk looks exactly as it did before this change

#### Scenario: Unknown name
- **WHEN** the theme name is not one of the built-in themes
- **THEN** the program exits with a non-zero status, names the unknown theme and lists the known ones, and does not open the workspace

### Requirement: Switching theme while the desk is open
Pressing `T` SHALL redraw the desk in the next theme, wrapping around after the last, and SHALL
show the new theme's name and how to start the desk in it. The choice SHALL NOT be stored.
`T` SHALL be ordinary text wherever text is being typed, SHALL do nothing while a dialog is
open, and SHALL never send a request to the daemon.

#### Scenario: Cycling
- **WHEN** the operator presses `T` repeatedly
- **THEN** the desk is redrawn in each theme in turn and returns to the first

#### Scenario: Keeping it
- **WHEN** the theme has just been switched
- **THEN** the status line names the theme and shows the flag and the environment variable that select it

#### Scenario: In the filter
- **WHEN** the list filter is being typed and the operator presses `T`
- **THEN** the letter is added to the filter and the theme does not change

#### Scenario: Under a confirmation
- **WHEN** a confirmation is open and the operator presses `T`
- **THEN** the theme does not change and the confirmation is neither confirmed nor closed

#### Scenario: While a confirmation is being prepared
- **WHEN** `T` is pressed after an action key and before its confirmation has appeared
- **THEN** the pending action is dropped, as it is by any key, and the status line still says so

### Requirement: A theme changes colour and nothing else
In every theme the desk SHALL show the same text in the same cells: the same panes, borders,
glyphs, markers and wording. The cues that do not depend on colour — the heavy border and
marker of the focused pane, the cursor glyph of the highlighted row, states spelled out in
words — SHALL be present in every theme. The colours used for danger, caution and success SHALL
keep those meanings in every theme.

#### Scenario: Same desk in every theme
- **WHEN** the same desk is drawn in two different themes
- **THEN** the two frames differ only in colour

#### Scenario: Highlighted row
- **WHEN** a row is highlighted in the focused pane
- **THEN** it is drawn in the theme's selection colours, or in reverse video when the theme defines none, and carries the cursor glyph either way

### Requirement: Themes are legible
Every colour a theme uses for text SHALL have a contrast ratio of at least 3:1 against the
background the theme is designed for, and the highlighted row's text at least 4.5:1 against the
highlight. The program SHALL NOT paint the terminal's background.

#### Scenario: Contrast
- **WHEN** a theme's text colours are compared with the background it is designed for
- **THEN** each has a contrast ratio of at least 3:1

### Requirement: No colour means no colour
When `NO_COLOR` is set, or the terminal reports no colour support, the program SHALL emit no
colour in any theme, and every theme SHALL draw the same frame.

#### Scenario: NO_COLOR with a theme
- **WHEN** `NO_COLOR` is set and `--theme dracula` is given
- **THEN** the output contains no colour sequence and the focused pane and highlighted row are still marked

#### Scenario: Switching without colour
- **WHEN** `T` is pressed on a terminal that shows no colour
- **THEN** the status line names the theme and says that the terminal shows no colour
