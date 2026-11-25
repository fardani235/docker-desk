# Spec Delta

## ADDED Requirements

### Requirement: Publishing to a registry is a typed, deliberate action
A push SHALL require the operator to type the tag being published and press Enter, after a
confirmation naming the full reference, the registry, the credential source and the remote
effect. No other input SHALL send anything. A push SHALL NOT change any local image or tag, and
read-only mode and `--no-registry` SHALL refuse it before any request is sent.

#### Scenario: Typed confirmation
- **WHEN** the push confirmation is shown
- **THEN** only the exact tag followed by Enter sends the push

#### Scenario: Read-only
- **WHEN** the desk is read-only
- **THEN** the push action is refused and no request is sent

#### Scenario: No registry
- **WHEN** the desk runs with `--no-registry`
- **THEN** the push is refused and no registry request is made

#### Scenario: Local state
- **WHEN** a push succeeds, fails or is cancelled
- **THEN** no local image or tag has been created, moved or removed

#### Scenario: Cancelled
- **WHEN** a push is cancelled
- **THEN** the desk does not claim the remote tag moved, and says uploaded layers may remain
