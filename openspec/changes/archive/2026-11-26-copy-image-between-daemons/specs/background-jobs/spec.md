# Spec Delta

## MODIFIED Requirements

### Requirement: Jobs belong to the daemon they run on
Every job SHALL record the daemon it acts on, and the Jobs pane SHALL show that daemon's name with
it. A job that spans two daemons — an image copy — SHALL record and show both. A job SHALL keep
running on its daemon when the operator moves focus to another daemon. A daemon with a running
job SHALL NOT be removed from the session until the job finishes or is cancelled; for a copy
this holds for both daemons.

#### Scenario: Focus moves while a pull runs
- **WHEN** a pull is running on one daemon and the operator focuses another daemon
- **THEN** the pull continues, and the Jobs pane still shows it with the daemon it runs on

#### Scenario: Removing a daemon with a job
- **WHEN** the operator tries to remove that daemon while the pull is running
- **THEN** the removal is refused and the job is named in the refusal

#### Scenario: Job ends on a background daemon
- **WHEN** a job finishes on an unfocused daemon
- **THEN** its outcome is shown in the Jobs pane with the daemon's name

#### Scenario: A copy spans two daemons
- **WHEN** an image copy runs from one daemon to another
- **THEN** the Jobs pane names both daemons with that job

#### Scenario: Removing either daemon of a copy
- **WHEN** the operator tries to remove the source or the target of a running copy
- **THEN** the removal is refused and the copy job is named in the refusal
