## ADDED Requirements

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
