## Purpose

Defines tracked background jobs, so that slow operations can be sent off while the operator
keeps working, with their progress and outcome visible in a corner of the desk.

## ADDED Requirements

### Requirement: Mutations run as tracked jobs
Every operation that changes the daemon SHALL run in the background as a job. Starting a job
SHALL NOT block input, navigation, live streams or other jobs.

#### Scenario: Pulling while working
- **WHEN** the operator starts an image pull and then moves around lists and reads logs
- **THEN** navigation and live views stay responsive while the pull continues

#### Scenario: Several jobs at once
- **WHEN** a second job is started while the first is still running
- **THEN** both run concurrently and both are shown

### Requirement: Jobs pane shows progress and outcome
The Jobs pane SHALL list recent jobs, newest first, each with what it is doing, the object it
targets, a state (running, succeeded, failed, cancelled), and — while running — progress when
the operation reports any. Finished jobs SHALL remain listed, up to a bounded number, until
cleared.

#### Scenario: Pull progress
- **WHEN** an image pull is running
- **THEN** its job shows a progress figure derived from the layers downloaded so far and the daemon's latest status line

#### Scenario: Multi-object job
- **WHEN** a job acts on several objects (a prune or a whole compose project)
- **THEN** its job shows how many of the objects have been processed

#### Scenario: Failure
- **WHEN** the daemon returns an error for a job
- **THEN** the job is shown as failed together with the daemon's error message, and the failure is also announced in the status line

#### Scenario: Partial failure
- **WHEN** a multi-object job fails on some objects and succeeds on others
- **THEN** the job continues with the remaining objects, ends as failed, and its output lists the result for each object

### Requirement: Job output can be read
The operator SHALL be able to open a job to read its complete output, including per-object
results and the full error text.

#### Scenario: Reading a failed job
- **WHEN** the operator highlights a failed job in the Jobs pane and presses Enter
- **THEN** a scrollable view shows everything the job reported, ending with the error

### Requirement: Running jobs can be cancelled
The operator SHALL be able to cancel a running job. Cancelling SHALL stop the job from issuing
further requests and SHALL be shown as cancelled; requests the daemon has already completed are
not undone, and the job output SHALL say what was already done.

#### Scenario: Cancelling a pull
- **WHEN** the operator cancels a running pull
- **THEN** the pull stops, and the job is shown as cancelled

#### Scenario: Cancelling a multi-object job
- **WHEN** the operator cancels a prune after three of ten objects were removed
- **THEN** no further objects are removed and the job output lists the three that were

#### Scenario: Failures before a cancel
- **WHEN** some objects of a multi-object job failed and the job is then cancelled
- **THEN** the job output still lists each failure and says how many succeeded, failed and were not attempted

#### Scenario: Cancel pressed on a job that fails by itself
- **WHEN** a job ends with an error of its own after cancel was requested
- **THEN** it is shown as failed with that error, not as cancelled

### Requirement: Finished jobs can be cleared
The operator SHALL be able to clear finished jobs from the Jobs pane; running jobs SHALL NOT be
cleared.

#### Scenario: Clearing
- **WHEN** the operator clears jobs while one job is running and two are finished
- **THEN** the two finished jobs disappear and the running job remains
