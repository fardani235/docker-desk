# image-changes Specification

## Purpose
Defines the Changes tab of a container: what differs between the image it runs and the image
its reference names now, how that is read, and when there is nothing to compare.

## Requirements

### Requirement: The tab is cheap until Enter
The Containers Details pane SHALL have a Changes tab directly after Info. Opening it SHALL send
at most four requests — an inspect and a history of each image — and SHALL NOT read an image
archive. Image facts SHALL be kept by image ID so a second visit sends none.

#### Scenario: A moved tag
- **WHEN** a container runs an image its reference no longer names
- **THEN** the tab shows both identities with age, size and the size difference, the shared
  layer prefix and the layers outside it with their instructions, and the config difference

### Requirement: Environment values are never shown
The config difference SHALL compare environment variables by name only, reporting names added
or removed. No environment value SHALL appear in the tab, a job, an error or a log.

#### Scenario: A value changed under the same name
- **WHEN** both images set a variable with different values
- **THEN** nothing is reported for it, and the tab states that values are not compared

### Requirement: Content comparison is a cancellable job
`Enter` SHALL start a job that reads the archive of each image not already analysed for its
platform, as Findings does, and SHALL report packages added, removed and version changed and
archive findings that appeared or disappeared. Reports SHALL be kept in Findings' cache. `c`
SHALL cancel it.

#### Scenario: Second open
- **WHEN** the tab is left and opened again after a comparison
- **THEN** the result is drawn in the same frame and no request is sent

### Requirement: Nothing to compare is explained
The tab SHALL say why there is nothing to compare when the container was created from a digest
or an image ID, when its reference no longer resolves, when it names the same image, when the
two images are for different platforms, or when a layer of either archive could not be read.
Every state SHALL say that restarting keeps the image and only re-creating takes the new one,
and SHALL say whether this tab can re-create it, with which key, or why it cannot.

#### Scenario: Read-only mode
- **WHEN** the desk runs with --read-only
- **THEN** the tab and its comparison complete and no mutating request is sent

#### Scenario: A newer build is offered
- **WHEN** the tag names a newer build of the same platform and the container's configuration is reproducible
- **THEN** the tab says the container can be re-created on that build with `A`

#### Scenario: The configuration cannot be reproduced
- **WHEN** the container carries a setting a re-create cannot reproduce
- **THEN** the tab names the setting, says the container cannot be re-created here, and offers no key

#### Scenario: A reference that cannot move
- **WHEN** the container was created from a digest or an image ID
- **THEN** the tab says restarting keeps the image and that this reference names no other build to re-create onto, and offers no key
