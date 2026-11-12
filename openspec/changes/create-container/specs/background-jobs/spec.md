# Spec Delta

## ADDED Requirements

### Requirement: A step waits for the steps it depends on
When a plan's steps depend on one another, a step SHALL be attempted only if every step it
depends on succeeded. A step that is not attempted SHALL be recorded in the job output with the
step it waited on. The job SHALL report how many steps succeeded, failed and were skipped.

#### Scenario: A failed prerequisite
- **WHEN** a plan's first step fails and two later steps depend on it
- **THEN** neither later step is attempted, the output names each one and the step it waited
  on, and the job ends as failed

#### Scenario: A cancel before a dependent step
- **WHEN** the operator cancels after the prerequisite succeeded but before the dependent step
  was attempted
- **THEN** the dependent step is not attempted and the output says what was already done

#### Scenario: Independent steps keep today's rule
- **WHEN** a multi-object job such as a prune has no dependencies between its steps and one
  object fails
- **THEN** the remaining objects are still processed

### Requirement: A step can address the object an earlier step produced
When a plan creates an object and a later step acts on it, the later step SHALL use the
identifier the daemon returned for the create, not a name looked up again. A second object
appearing under the same name between the two steps SHALL NOT be affected.

#### Scenario: Start the container just created
- **WHEN** a create step returns a container ID and the next step starts it
- **THEN** the start request names that ID

#### Scenario: The container is renamed between the steps
- **WHEN** another tool renames the container after its create step succeeded and before the
  start step runs
- **THEN** the start request still names the returned ID, so the created container is started
  under its new name
