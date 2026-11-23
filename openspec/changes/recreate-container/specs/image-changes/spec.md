# Spec Delta

## MODIFIED Requirements

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
