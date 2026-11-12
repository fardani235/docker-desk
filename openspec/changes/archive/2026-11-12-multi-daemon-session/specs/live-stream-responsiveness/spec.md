# Spec Delta

## MODIFIED Requirements

### Requirement: Streams end with their purpose
A live stream SHALL be closed when it is no longer displayed or needed: the log stream when the
highlight moves away or the logs view is left, a statistics stream when its container stops or
disappears, all statistics and log streams of a daemon when focus moves to another daemon, and all
streams on quit. An unfocused daemon SHALL keep only its event stream and its ping.

#### Scenario: Leaving the logs view
- **WHEN** the operator switches the Details pane away from logs
- **THEN** the log stream to the daemon is closed

#### Scenario: Many running containers
- **WHEN** more containers are running than the per-container statistics limit
- **THEN** statistics are streamed for at most that number of containers, always including the highlighted one, and the remaining rows show that statistics are not being collected

#### Scenario: Focus leaves a daemon
- **WHEN** the operator focuses another daemon
- **THEN** the statistics and log streams of the previous daemon are closed, and only its event stream remains open
