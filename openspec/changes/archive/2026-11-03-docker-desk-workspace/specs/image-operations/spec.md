## Purpose

Defines the image actions available from the desk: pulling by name, removing, and pruning
unused images.

## ADDED Requirements

### Requirement: Pull an image by name
The operator SHALL be able to pull an image by typing its reference. The form SHALL state that
the image will be downloaded onto the displayed daemon. A reference without a tag or digest
SHALL be pulled as `:latest` only, never as all tags. The pull SHALL run as a job with progress.

#### Scenario: Pulling
- **WHEN** the operator opens the pull form, types `alpine:3.20` and presses Enter
- **THEN** a job pulls that reference, shows progress, and the image appears in the list when the pull completes

#### Scenario: No tag given
- **WHEN** the operator types `alpine`
- **THEN** the form shows that `alpine:latest` will be pulled, and exactly that reference is pulled

#### Scenario: Empty or malformed reference
- **WHEN** the operator presses Enter with an empty or syntactically invalid reference
- **THEN** nothing is sent and the form says what is wrong

#### Scenario: Pull fails
- **WHEN** the registry or daemon reports an error, for example that the image does not exist
- **THEN** the job is shown as failed with that error and the desk stays usable

### Requirement: Remove an image
The operator SHALL be able to remove the highlighted image row, following the mutation-safety
rules. A tagged row SHALL be removed by its reference; an untagged row by its ID. Removal SHALL
never be forced.

#### Scenario: Removing the only tag
- **WHEN** the operator removes the only reference of an image no container uses and completes the typed confirmation
- **THEN** the image is deleted and disappears from the list, and the job output lists what the daemon untagged and deleted

#### Scenario: Removing one of several tags
- **WHEN** the image has other tags
- **THEN** only the chosen reference is removed and the other rows remain

#### Scenario: Image in use
- **WHEN** a container was created from the image
- **THEN** removal is refused and the containers are named

### Requirement: Prune unused images
The operator SHALL be able to prune images, choosing between dangling images only (the default)
and all images not used by any container. The prune SHALL follow the exact-set prune rule.

#### Scenario: Dangling only
- **WHEN** the operator opens image prune
- **THEN** the preview lists only untagged images that no container uses

#### Scenario: All unused
- **WHEN** the operator switches the preview to all unused images
- **THEN** the preview is recomputed and lists every image that no container, running or stopped, was created from

#### Scenario: Confirmed prune
- **WHEN** the operator completes the typed confirmation
- **THEN** a job removes each listed image in turn, reports progress, and the job output shows per-image results and the space reclaimed
