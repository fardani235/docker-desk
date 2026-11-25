# image-operations Specification

## Purpose
Defines the image actions available from the desk: pulling by name, removing, pruning
unused images, giving an image another local tag, and removing one of an image's tags while
the image stays.

## Requirements

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

### Requirement: Pull one build by its digest
The repository browser's Enter and Images › Upstream's `P` on a moved verdict SHALL open one
confirmation that pulls exactly `name:tag@sha256:<digest>`, shows that full reference, says the
digest and not the tag is fetched, and is typed with the tag.

#### Scenario: Pulling a listed tag by digest
- **WHEN** the operator presses Enter on `3.28.3`, types `3.28.3` and presses Enter
- **THEN** one pull of `host/acme/api:3.28.3@sha256:…` runs as a job, and the daemon is asked for the digest

#### Scenario: The wrong word
- **WHEN** the operator types anything but the tag and presses Enter
- **THEN** nothing is sent and the dialog says so

#### Scenario: From a moved verdict
- **WHEN** Images › Upstream shows moved for an image and the operator presses `P`
- **THEN** the same confirmation opens for the digest the check read, typed with the tag

#### Scenario: No build for the daemon's platform
- **WHEN** the tag's index publishes no build for the daemon's platform
- **THEN** the confirmation refuses, and nothing is sent

### Requirement: Tag an image
Images SHALL offer `t` on the highlighted image, including an untagged one, and open a form
naming the image and its short ID, its current references and the target. The tag SHALL be
created locally by the exact image ID, with no registry contacted, as a job. Read-only SHALL
refuse it, a selection SHALL be refused one-at-a-time, and `--no-registry` SHALL allow it.

#### Scenario: The key
- **WHEN** the operator presses `t` on an image
- **THEN** the tag form opens, and no request has been sent

#### Scenario: A tag is given
- **WHEN** the operator types `acme/api:3.29.0` and presses Enter
- **THEN** one local tag request creates that reference for the highlighted image, as a job

#### Scenario: An untagged image
- **WHEN** the highlighted row is an untagged image
- **THEN** its ID is the source and the target tag is created for it

#### Scenario: Read-only
- **WHEN** the desk is read-only
- **THEN** the action is refused and nothing is sent

### Requirement: A tag target is a valid local reference
The form SHALL normalise the target as a named reference, SHALL read a reference with no tag
as `:latest`, and SHALL refuse a digest target or an invalid reference before any request.

#### Scenario: No tag
- **WHEN** the operator types `acme/api`
- **THEN** the form shows `acme/api:latest` and exactly that tag is created

#### Scenario: A digest
- **WHEN** the target contains `@sha256:`
- **THEN** the form refuses it and sends nothing

#### Scenario: Invalid
- **WHEN** the target cannot be parsed as an image reference
- **THEN** the form says why and sends nothing

### Requirement: A tag move is named and re-checked
When the target already names another image the form SHALL name that image and say the tag
will move; when it already names this image the form SHALL say so and tag nothing. Immediately
before the request the desk SHALL re-inspect the target and SHALL tag nothing if it now names
a different image from the one the form showed.

#### Scenario: Another image holds the tag
- **WHEN** the target currently names image B and the form is for image A
- **THEN** the form names B, says the tag will move to A, and Enter may complete that move

#### Scenario: Already this image
- **WHEN** the target already names the highlighted image
- **THEN** the form says it is already tagged, and no request is made

#### Scenario: The target moved meanwhile
- **WHEN** the target named no image or image B when the form was read, and names image C by
  the time the job inspects it
- **THEN** the job tags nothing and says the target changed

### Requirement: Untag a reference without deleting the image
Images SHALL offer `u` on a tagged row and SHALL remove that one reference only when at least
one other reference remains, requiring the operator to type the tag and press Enter. The image
data SHALL stay, and the reference SHALL be pinned to the image that was shown. On the image's
last reference the action SHALL refuse and point to `d remove`, because removing it would
delete the image. Read-only SHALL refuse it, a selection SHALL be refused one-at-a-time, and
`--no-registry` SHALL allow it.

#### Scenario: Another reference remains
- **WHEN** an image has `acme/api:3.29.0` and `acme/api:stable`, and the operator untags `3.29.0`
- **THEN** only that reference is removed, and the image remains under `stable`

#### Scenario: The word
- **WHEN** the confirmation is armed and the operator types `3.29.0` and presses Enter
- **THEN** the reference is removed

#### Scenario: Wrong word
- **WHEN** the operator types anything else and presses Enter
- **THEN** nothing is sent and the dialog says the text does not match

#### Scenario: The last tag
- **WHEN** the highlighted reference is the image's only tag
- **THEN** the desk refuses before opening a confirmation, says removing it would delete the
  image, and points to `d remove`; nothing is sent

#### Scenario: Untagged
- **WHEN** the highlighted row has no reference
- **THEN** untag is refused and nothing is sent

#### Scenario: The tag moved
- **WHEN** the reference no longer names the image that was shown
- **THEN** nothing is removed and the job says the tag moved

#### Scenario: Read-only
- **WHEN** the desk is read-only
- **THEN** the action is refused and nothing is sent
