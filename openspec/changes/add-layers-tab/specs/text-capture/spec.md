# Spec Delta

## MODIFIED Requirements

### Requirement: A key that captures the focused pane's text
`W` SHALL open the evidence dialog on the text of the focused pane, and the file SHALL be written
only after that dialog's confirmation. `W` SHALL be offered in Info, Inspect, Layers, Logs,
Processes, Changes and Declared, in the Events pane, in the Jobs pane for the selected job's
output, and in the file reader. It SHALL not be offered on Stats, Findings, Packages, Upstream or
the List.

#### Scenario: A Details tab
- **WHEN** the Details pane is focused on Logs and `W` is pressed
- **THEN** a dialog opens with a path and one line saying what the file will hold, and nothing is written until it is confirmed

#### Scenario: The Events pane
- **WHEN** the Events pane has focus and `W` is pressed
- **THEN** the dialog offers the events the pane holds

#### Scenario: The Jobs pane
- **WHEN** a finished job is selected in the Jobs pane and `W` is pressed
- **THEN** the dialog offers that job's output

#### Scenario: The Layers tab
- **WHEN** the Details pane is focused on Layers and `W` is pressed
- **THEN** the dialog opens on a file that will hold every build step, and nothing is written until it is confirmed

#### Scenario: A pane with no capture
- **WHEN** the List pane or the Stats tab has focus and `W` is pressed
- **THEN** no dialog opens, nothing is written, and no other action runs

### Requirement: The prefilled name
The path SHALL be prefilled as `~/docker-desk-<kind>-<object>-<YYYYMMDD>.<ext>`, where the kind is
`info`, `inspect`, `layers`, `logs`, `processes`, `changes`, `declared`, `events`, `output` or
`file`, and the object is the highlighted object's name, or for the Events pane the focused
daemon's context name (`engine` when there is none).

#### Scenario: A container's logs
- **WHEN** the Logs view of `shop-web-1` is opened for capture
- **THEN** the path is prefilled on `~/docker-desk-logs-shop-web-1-<date>.txt`

#### Scenario: An image's build
- **WHEN** the Layers tab of `shop/api:dev` is captured
- **THEN** the path is prefilled on `~/docker-desk-layers-shop-api-dev-<date>.txt`

#### Scenario: The Events pane
- **WHEN** the Events pane is captured on a daemon whose context is `prod`
- **THEN** the prefilled name carries `events-prod`
