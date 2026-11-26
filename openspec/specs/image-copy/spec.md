# image-copy Specification

## Purpose

Defines copying one highlighted image from the focused daemon to another connected daemon of
the same session, without a registry and without a file on this machine: the action, the
destination choice, the typed confirmation, the streaming job, progress, cancellation,
verification and the two-daemon safety rules.

## Requirements

### Requirement: The copy action is on the highlighted image

Images SHALL offer `C` (copy to daemon…) for the highlighted row, in the action menu and as the
direct key from the List pane. It SHALL act on one image only, SHALL send nothing until the
typed confirmation is completed, and SHALL be refused in read-only mode before any request.

#### Scenario: The key
- **WHEN** an image is highlighted and the operator presses `C`
- **THEN** the destination chooser opens and no request has been sent

#### Scenario: Several selected
- **WHEN** a selection exists and the operator presses `C`
- **THEN** the desk says the action works on one image at a time, and nothing is sent

#### Scenario: Read-only
- **WHEN** the desk is in read-only mode
- **THEN** the action is refused and no request is sent to either daemon

#### Scenario: No registry configured to talk to
- **WHEN** the desk runs with `--no-registry`
- **THEN** the copy remains available, because no registry request is made

### Requirement: The destination is one connected daemon of the session

The action SHALL list every connected member of the session except the focused source, with its
name, endpoint, engine name and version. A member that is disconnected SHALL be listed as
unusable with the reason and SHALL NOT be chosen. When no other connected daemon exists the
action SHALL refuse and name the picker key that adds one.

#### Scenario: One daemon only
- **WHEN** the session holds only the focused daemon and the operator presses `C`
- **THEN** the desk says a copy needs a second connected daemon, names the picker key, and nothing is sent

#### Scenario: Choosing a destination
- **WHEN** two daemons are connected and the operator chooses the second
- **THEN** the confirmation opens for that target and no image request has been sent

#### Scenario: A disconnected destination
- **WHEN** a member is listed but disconnected
- **THEN** it is shown as unusable with the reason and cannot be chosen

#### Scenario: The chosen daemon is removed before confirming
- **WHEN** the target member is removed from the session while the confirmation is open
- **THEN** the confirmation says the target is gone and offers no way to proceed

### Requirement: The confirmation names both daemons and the exact transfer

The typed confirmation SHALL name the source and target endpoints as the operator named them,
the reference or ID that will be copied, the image's short ID, the platform, the listed size,
and that the archive streams directly between the daemons with no file here and no registry
request. Its word SHALL be the source tag, or the short image ID for an untagged row. Every
other input SHALL send nothing.

#### Scenario: A tagged image
- **WHEN** the confirmation opens for `host/acme/api:3.28.3`
- **THEN** it names both daemons, the full reference, the short ID and the platform, and the typed word is `3.28.3`

#### Scenario: An untagged image
- **WHEN** the confirmation opens for a row with no reference
- **THEN** it says the image will arrive untagged and the typed word is its short ID

#### Scenario: The wrong word
- **WHEN** the operator types anything else and presses Enter
- **THEN** nothing is sent and the dialog says the text does not match

#### Scenario: `y` is not consent
- **WHEN** the operator presses `y` or Enter without the word
- **THEN** nothing is sent and the dialog stays open

### Requirement: A target tag that will move is named before it does

Before the confirmation is shown the desk SHALL read the target daemon for the reference the
copy will carry. When the target holds that reference for a different image it SHALL name that
image's short ID and state that the load will move the tag; when it holds the same image the
plan SHALL be blocked as already present; when it holds no such reference it SHALL say the
tag will be created.

#### Scenario: The tag is new on the target
- **WHEN** the target has no `host/acme/api:3.28.3`
- **THEN** the confirmation says the tag will be created there

#### Scenario: The tag names another image
- **WHEN** the target's `host/acme/api:3.28.3` names a different image
- **THEN** the confirmation names that image's short ID, says the tag will move, and requires the typed word

#### Scenario: The image is already there
- **WHEN** the target already holds the same image under that tag
- **THEN** the plan is blocked as already present and nothing can be sent

#### Scenario: The target cannot be read
- **WHEN** inspecting the target fails
- **THEN** the dialog states the failure and offers no way to proceed

### Requirement: The copy streams the shown image and platform directly

The job SHALL re-inspect the source reference immediately before reading and SHALL send
nothing if it no longer names the shown image. It SHALL stream that image's archive, for the
platform the image was inspected as, straight into the target daemon's load request: no
offer of another tag, no other platform, no intermediate file on this machine, and no registry
request. The source SHALL only be read.

#### Scenario: Tag moved
- **WHEN** the source reference now names another image
- **THEN** nothing is streamed and the job says the tag moved

#### Scenario: Gone
- **WHEN** the source image no longer exists
- **THEN** nothing is streamed and the job reports it

#### Scenario: Platform
- **WHEN** the image is inspected as `linux/arm64/v8`
- **THEN** the confirmation and the job name that platform, and only that platform's archive is sent

#### Scenario: The source is unchanged
- **WHEN** a copy succeeds, fails or is cancelled
- **THEN** no image or tag on the source daemon has been created, moved or removed

### Requirement: The copy runs as one cancellable two-daemon job

The copy SHALL run as a job reporting bytes streamed and the target's latest load status.
Cancelling SHALL close both streams and SHALL say that layers already accepted by the target
may remain. A failure SHALL name which daemon and which direction failed, and SHALL NOT claim
that an image or a moved tag exists on the target unless the load completed and was verified.

#### Scenario: Success
- **WHEN** the load ends normally and verification passes
- **THEN** the job reports the bytes streamed, the platform, the target reference and both daemons

#### Scenario: Cancelled
- **WHEN** the operator cancels during the stream
- **THEN** both streams are closed and the job says layers already accepted may remain while no image or moved tag is claimed

#### Scenario: The source fails mid-stream
- **WHEN** the source daemon's stream ends with an error
- **THEN** the job fails naming the source and the target stream is closed

#### Scenario: The target refuses or fails
- **WHEN** the target daemon refuses the load or ends it with an error
- **THEN** the job fails with the target's message and the source stream is closed

#### Scenario: The target disconnects
- **WHEN** the target daemon becomes unreachable during the copy
- **THEN** the job fails naming the target and saying how far the stream had got

### Requirement: The loaded image is verified on the target

After the load stream ends the job SHALL inspect the target for the reference (or image ID) it
just loaded and SHALL require the same layer chain and platform as the source. It SHALL report
both image IDs, and SHALL succeed when the two stores name the same build differently. Missing
or different content SHALL fail the job and name what the target holds instead.

#### Scenario: The same build, whatever the store calls it
- **WHEN** the target reports the source's layers and platform under a different image ID
- **THEN** the job succeeds, says the image is present, and reports both IDs

#### Scenario: Different content
- **WHEN** the target's image under the reference has a different layer chain or platform
- **THEN** the job fails and names the difference

#### Scenario: Nothing to inspect
- **WHEN** the target's load stream ended but no image exists under the reference
- **THEN** the job fails and says the load did not produce the image

### Requirement: Both daemons are held for the job

While a copy runs neither the source nor the target SHALL be removed from the session. A removal
attempt SHALL be refused and SHALL name the copy job. Moving focus SHALL NOT interrupt the copy,
and the Jobs pane SHALL name both daemons.

#### Scenario: Removing the source
- **WHEN** the operator tries to remove the source daemon while the copy runs
- **THEN** the removal is refused and the job is named

#### Scenario: Removing the target
- **WHEN** the operator tries to remove the target daemon while the copy runs
- **THEN** the removal is refused and the job is named

#### Scenario: Focus moves
- **WHEN** the operator focuses a third daemon while the copy runs
- **THEN** the copy continues and the Jobs pane still shows it with both daemon names
