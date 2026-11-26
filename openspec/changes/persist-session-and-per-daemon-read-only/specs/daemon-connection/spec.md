# Spec Delta

## MODIFIED Requirements

### Requirement: Read-only mode
Each member of the session SHALL be either read-write or read-only. A read-only member SHALL
refuse every mutating action, and the shell, before any request for it is sent to its daemon,
and SHALL show that it is read-only whenever it is named. Starting with `--read-only` SHALL
lock the whole session read-only: every member, including daemons added later and ones restored
from save, is read-only and the desk SHALL NOT offer a way to change that for the run.

#### Scenario: Mutation attempted in read-only mode
- **WHEN** the focused member is read-only and the operator triggers any action that would change or delete something, or open a shell
- **THEN** the program states that this daemon is read-only and sends nothing to it

#### Scenario: Mutation attempted on a read-write member
- **WHEN** the focused member is read-write and another member of the same session is read-only
- **THEN** the action on the read-write member proceeds to its ordinary confirmation

#### Scenario: Daemon added in read-only mode
- **WHEN** the operator adds a daemon from the picker while `--read-only` is in force
- **THEN** the new daemon is read-only too, and the picker says so before the daemon is added

#### Scenario: Observation in read-only mode
- **WHEN** the operator browses lists, details, logs, stats and events on a read-only member
- **THEN** they behave exactly as on a read-write member

#### Scenario: Read-only cannot be unlocked under the flag
- **WHEN** the program was started with `--read-only` and the operator tries to change a member's mode
- **THEN** the mode does not change, the desk says the run is locked read-only, and nothing is sent

#### Scenario: A read-write member with a read-only target
- **WHEN** an action would send a request to a daemon other than the focused one, such as an image copy
- **THEN** that daemon's own mode decides, and a read-only target refuses the action before anything is sent to it
