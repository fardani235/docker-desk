# Spec Delta

## MODIFIED Requirements

### Requirement: Choosing a theme
The theme SHALL come from the `--theme` flag, else `DOCKER_DESK_THEME`, else the state file,
else the automatic choice (`dark` for a dark or unknown background, `light` for a light one).
`auto` means that automatic choice. Names SHALL be matched without regard to case. An unknown
name from the flag or environment SHALL be refused at launch with the known names, without
opening the workspace; an unknown name in the state file SHALL be ignored with one notice.

#### Scenario: Flag
- **WHEN** the program is started with `--theme nord`
- **THEN** the desk is drawn in the Nord palette

#### Scenario: Environment
- **WHEN** `DOCKER_DESK_THEME=light` is set and no `--theme` is given
- **THEN** the desk is drawn in the light palette

#### Scenario: Flag over environment
- **WHEN** both `--theme` and `DOCKER_DESK_THEME` are given
- **THEN** the flag's theme is used

#### Scenario: Stored theme
- **WHEN** neither the flag nor the environment names a theme and the state file stores `gruvbox`
- **THEN** the desk is drawn in the Gruvbox palette

#### Scenario: Flag over stored theme
- **WHEN** the state file stores one theme and the program is started with another
- **THEN** the flag's theme is used and is stored for the next launch

#### Scenario: Environment over stored theme
- **WHEN** the state file stores one theme and `DOCKER_DESK_THEME` names another
- **THEN** the environment's theme is used

#### Scenario: Unknown stored name
- **WHEN** the state file stores a name that is not a built-in theme
- **THEN** the automatic choice is used, the rest of the state is applied, and one notice names the unknown theme

#### Scenario: No setting
- **WHEN** neither flag nor environment is given, no state file is in use, and the terminal's background is dark or cannot be determined
- **THEN** the desk looks exactly as it did before this change

#### Scenario: Unknown name
- **WHEN** the theme name given by flag or environment is not one of the built-in themes
- **THEN** the program exits with a non-zero status, names the unknown theme and lists the known ones, and does not open the workspace

### Requirement: Switching theme while the desk is open
Pressing `T` SHALL redraw the desk in the next theme, wrapping around after the last, and SHALL
show the new theme's name and that it has been kept. The choice SHALL be stored in the state
file and used by the next launch when neither the flag nor the environment selects a theme; it
SHALL NOT be stored when `--no-state` is in force. `T` SHALL be ordinary text wherever text is
being typed, SHALL do nothing while a dialog is open, and SHALL never send a request to the
daemon.

#### Scenario: Cycling
- **WHEN** the operator presses `T` repeatedly
- **THEN** the desk is redrawn in each theme in turn and returns to the first

#### Scenario: Keeping it
- **WHEN** the theme has just been switched
- **THEN** the status line names the theme and says it has been saved for the next launch

#### Scenario: Next launch
- **WHEN** the operator quits after switching to `dracula` and starts the desk again with no theme setting
- **THEN** the desk is drawn in `dracula`

#### Scenario: --no-state
- **WHEN** the program was started with `--no-state` and the operator switches theme
- **THEN** the desk is redrawn, the status line says the choice is not being kept, and no state file is written

#### Scenario: In the filter
- **WHEN** the list filter is being typed and the operator presses `T`
- **THEN** the letter is added to the filter and the theme does not change

#### Scenario: Under a confirmation
- **WHEN** a confirmation is open and the operator presses `T`
- **THEN** the theme does not change and the confirmation is neither confirmed nor closed

#### Scenario: While a confirmation is being prepared
- **WHEN** `T` is pressed after an action key and before its confirmation has appeared
- **THEN** the pending action is dropped, as it is by any key, and the status line still says so
