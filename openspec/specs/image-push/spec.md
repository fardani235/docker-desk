# image-push Specification

## Purpose

Defines pushing one highlighted image's existing tag to its registry: the action, the typed
confirmation, the pinned reference, credential handling, progress, cancellation and failure
reporting.

## Requirements

### Requirement: The push action is on the highlighted tag
Images SHALL offer `p` (push) for the highlighted row, in the action menu and as the direct
key. It SHALL act on one image's existing tag only: an untagged row SHALL be refused with a
reason, a selection SHALL be refused as one-at-a-time, and `--no-registry` SHALL refuse
without sending anything. No request SHALL be sent until the confirmation is completed.

#### Scenario: The key
- **WHEN** the row `host/acme/api:3.28.3` is highlighted and the operator presses `p`
- **THEN** a typed confirmation opens and no push request has been sent

#### Scenario: Untagged
- **WHEN** the highlighted row has no reference
- **THEN** the desk says the image has no tag to push, and nothing is sent

#### Scenario: Several selected
- **WHEN** a selection exists and the operator presses `p`
- **THEN** the desk refuses it as one image at a time and nothing is sent

#### Scenario: No registry
- **WHEN** the desk runs with `--no-registry`
- **THEN** the push action is refused and no registry request is made

#### Scenario: Read-only
- **WHEN** the desk is read-only
- **THEN** the action is refused and no push request is sent

### Requirement: The confirmation names the publish and is typed with the tag
The confirmation SHALL name the full reference, the image's short ID, the registry, the
credential source, the layers that will be uploaded, and that the remote tag will be created
or moved and cannot be undone from this desk. It SHALL require the operator to type the tag
exactly and press Enter; every other input SHALL send nothing. It SHALL state that the local
image and tag are not changed and that nothing is built.

#### Scenario: The tag matches
- **WHEN** the confirmation is armed and the operator types `3.28.3` and presses Enter
- **THEN** exactly that reference is pushed

#### Scenario: The wrong word
- **WHEN** the operator types anything else and presses Enter
- **THEN** nothing is sent and the dialog says the text does not match

#### Scenario: `y` is not consent
- **WHEN** the operator presses `y` or Enter without the tag
- **THEN** nothing is sent and the dialog stays open

#### Scenario: Effect
- **WHEN** the confirmation is shown
- **THEN** it says the remote tag may be created or moved, that this cannot be undone here, and that the local image is unchanged

#### Scenario: Unencrypted link
- **WHEN** the focused daemon is reached over an unencrypted `tcp://` connection
- **THEN** the confirmation warns that the credential will be sent to that daemon over the unencrypted connection

### Requirement: The credential comes from the Docker configuration
The desk SHALL resolve the target registry's credential the way the Docker CLI does — auths,
`credsStore`, `credHelpers` — SHALL name only its source, and SHALL never show or log a
username, password or token. A helper failure SHALL block the push before any request. No
credential SHALL be stated as such, and the registry may then refuse. The credential SHALL be
resolved again when the push starts and sent to the daemon as `X-Registry-Auth`.

#### Scenario: A stored credential
- **WHEN** the configuration has an entry for the registry
- **THEN** the dialog names its source and the push sends that credential to the daemon

#### Scenario: A helper fails
- **WHEN** the credential helper fails
- **THEN** the dialog refuses, names how the helper failed without its output, and nothing is sent

#### Scenario: No credential
- **WHEN** no credential is found
- **THEN** the dialog says so, and the push still may be attempted for a registry that allows it

#### Scenario: Values never surface
- **WHEN** the dialog and the job output are inspected
- **THEN** no credential value appears in either

### Requirement: The existing tag is pinned and pushed unchanged
Immediately before the push the desk SHALL re-inspect the reference and SHALL send nothing if
it no longer names the image that was confirmed. It SHALL push that reference and tag only: it
SHALL NOT retag, push another tag, push all tags, or build anything.

#### Scenario: The tag moved
- **WHEN** the reference now names another image
- **THEN** nothing is pushed and the job says the tag moved

#### Scenario: One tag
- **WHEN** the image carries several tags and one row is pushed
- **THEN** only that reference is uploaded

#### Scenario: Gone
- **WHEN** the image no longer exists
- **THEN** nothing is sent and the job reports it

### Requirement: The push is a cancellable job with the daemon's result
The push SHALL run as a job reporting layer progress and the daemon's final digest. Cancelling
SHALL close the stream and say that uploaded layers may remain while the tag may not have
moved. A registry or daemon refusal SHALL fail the job with the daemon's message and SHALL
leave the local image and tag untouched.

#### Scenario: Success
- **WHEN** the registry accepts the push
- **THEN** the job reports progress, the final digest and that the local image is unchanged

#### Scenario: Cancelled
- **WHEN** the operator cancels
- **THEN** the stream is closed and the job says layers already uploaded may remain and the tag may not have moved

#### Scenario: Refused
- **WHEN** the registry refuses the credential or the repository
- **THEN** the job fails with the daemon's message and nothing local changes
