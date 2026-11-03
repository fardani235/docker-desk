## Purpose

Defines the bounds that keep the desk responsive while several live streams — logs, per-container
stats, engine events, job progress — run at once, so that a noisy stream cannot make the
interface stutter or grow without limit.

## ADDED Requirements

### Requirement: Input latency is independent of stream volume
The time between a key press and the screen reflecting it SHALL NOT grow with the rate at which
live streams produce data.

#### Scenario: Navigating under a log flood
- **WHEN** the followed container writes at least 50,000 log lines per second while stats and events streams are also active
- **THEN** moving the highlight or changing pane takes effect within 100 milliseconds

#### Scenario: Flood in an unfocused pane
- **WHEN** a noisy stream is displayed in a pane that does not have focus
- **THEN** typing, navigation and dialogs in the focused pane are unaffected

### Requirement: Screen updates are rate-limited
The screen SHALL be redrawn at a bounded rate regardless of how much stream data arrives, and
SHALL NOT be redrawn when nothing visible changed.

#### Scenario: Thousands of lines between frames
- **WHEN** thousands of log lines arrive between two redraws
- **THEN** one redraw shows the latest lines; no redraw is performed per line

#### Scenario: Idle desk
- **WHEN** no stream produces data and no key is pressed
- **THEN** the desk does not redraw continuously

### Requirement: Memory use of live data is bounded
Each live view SHALL retain a bounded amount of data: a maximum number of log lines, a maximum
length per line, a maximum number of events, and a fixed-length history of statistics samples.
When a bound is reached the oldest data SHALL be discarded.

#### Scenario: Long-running follow
- **WHEN** a container is followed while it writes more lines than the log buffer holds
- **THEN** the oldest lines are discarded and the buffer does not grow

#### Scenario: A line without end
- **WHEN** a container writes megabytes without a line break
- **THEN** the output is split into bounded-length lines and memory does not grow without limit

### Requirement: Streams are independent
A stalled, failing or noisy stream SHALL NOT delay, block or terminate any other stream, list
refresh or job.

#### Scenario: Stats stream stalls
- **WHEN** one container's statistics stream stops delivering samples
- **THEN** other containers' statistics, logs, events and jobs continue to update

#### Scenario: A stream errors
- **WHEN** a log or statistics stream ends with an error
- **THEN** only the view showing that stream reports it and the rest of the desk is unaffected

### Requirement: Streams end with their purpose
A live stream SHALL be closed when it is no longer displayed or needed: the log stream when the
highlight moves away or the logs view is left, a statistics stream when its container stops or
disappears, and all streams on quit.

#### Scenario: Leaving the logs view
- **WHEN** the operator switches the Details pane away from logs
- **THEN** the log stream to the daemon is closed

#### Scenario: Many running containers
- **WHEN** more containers are running than the per-container statistics limit
- **THEN** statistics are streamed for at most that number of containers, always including the highlighted one, and the remaining rows show that statistics are not being collected
