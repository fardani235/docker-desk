# Spec Delta

## ADDED Requirements

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
