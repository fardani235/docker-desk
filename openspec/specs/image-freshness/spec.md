# image-freshness Specification

## Purpose
Defines whether the tag an image carries names a later build upstream: what is asked, how the
two images are compared, where the credentials come from, what happens when the registry
cannot be reached, and what the desk refuses to claim.

## Requirements

### Requirement: The check is asked for, and only reads
The desk SHALL ask a registry only when the operator opens the Images › Upstream tab on an
image or asks for a batch. It SHALL send GET and HEAD requests only, SHALL NOT write anything
to disk, and SHALL complete in read-only mode.

#### Scenario: Opened in read-only mode
- **WHEN** the desk runs with --read-only and the operator opens the Upstream tab
- **THEN** the check runs and reports a verdict, and no mutating request is sent

#### Scenario: No request on a plain list refresh
- **WHEN** the Images list refreshes with no tab open and no batch asked for
- **THEN** no registry is contacted

### Requirement: A verdict names what was seen, never "newer" or "stale"
The verdict SHALL be one of current, moved, gone, unknown or not applicable. For moved it SHALL
report the local and upstream build times when known, which time is later and by how much, and
how many leading layers the two images share. It SHALL NOT rank the images or claim that one
ought to be used.

#### Scenario: The tag names a later build
- **WHEN** the upstream config's created time is after the local image's
- **THEN** the tab shows both times, the difference, the shared layer prefix and the count that
  differs, and says the upstream build is later without calling it newer

#### Scenario: The upstream build is older or identical
- **WHEN** the upstream created time is earlier than or equal to the local image's
- **THEN** the tab says so plainly and still reports the layer difference

#### Scenario: Both build times are unusable
- **WHEN** the upstream config carries no created time, or carries the epoch
- **THEN** the verdict says the build time is unknown and no direction is inferred

### Requirement: The image is identified by its recorded digest
The comparison SHALL use the local image's RepoDigests for the repository, and SHALL NOT use the
image ID as the local digest, because a classic image store records a different digest there.

#### Scenario: A classic image store
- **WHEN** the image ID differs from the recorded repository digest
- **THEN** the comparison uses the recorded digest and reaches the same verdict as on a
  containerd store

#### Scenario: A locally built or digest-pinned image
- **WHEN** the image has no recorded repository digest, or is referenced by digest only
- **THEN** the verdict is not applicable and the tab says there is no tag to follow

### Requirement: Credentials come from the Docker configuration
Registry credentials SHALL be read from the operator's Docker configuration (`auths`,
`credsStore`, `credHelpers`, or `$DOCKER_CONFIG`) under the Docker CLI's server key, scoped to the
exact registry host, preferring the entry filed under that exact key over any entry that only
shares its host. No credential value SHALL appear in the tab, a job line, an error, a log or any
written file.

#### Scenario: Docker Hub behind its legacy key
- **WHEN** the reference resolves to Docker Hub and the configuration holds the index address
- **THEN** the credential is found and used, and the host contacted is the registry

#### Scenario: Sibling entries sharing the host
- **WHEN** the configuration holds an entry for `https://index.docker.io/v1/` and entries for
  sibling keys under it, such as `https://index.docker.io/v1/access-token` and
  `https://index.docker.io/v1/refresh-token`
- **THEN** every resolution uses the entry under the exact server key, and no sibling entry is
  ever sent to a registry or its token service

#### Scenario: A legacy entry without the CLI's key
- **WHEN** a host's only entry is filed under a key that differs from the server key but names
  the same host
- **THEN** that entry is used, as the CLI's legacy fallback does

#### Scenario: A helper that does not answer
- **WHEN** a configured credential helper is missing, exits nonzero or exceeds its time
- **THEN** the verdict is unknown and names the helper and the failure, not its output

#### Scenario: A private repository without credentials
- **WHEN** a registry refuses anonymous access and no credential exists for its host
- **THEN** the verdict is unknown and says the host refused anonymous access

### Requirement: The daemon answers when the desk cannot
When the desk cannot obtain an answer — because of a name, connection or TLS failure, a timeout,
a quota refusal, or an unreadable response — it SHALL ask the daemon's distribution endpoint
with the same credential. A definitive answer, including a refusal of the credential, SHALL NOT
be retried through the daemon.

#### Scenario: The desk's network is the wrong network
- **WHEN** the registry cannot be reached from the desk but the daemon can reach it
- **THEN** the verdict is reported as having come through the daemon, and the build time and
  layer comparison are reported as unknown rather than invented

#### Scenario: Both paths fail
- **WHEN** the desk and the daemon both fail to obtain an answer
- **THEN** the verdict is unknown and shows both reasons

#### Scenario: The daemon is too old to be asked
- **WHEN** the daemon does not serve the distribution endpoint
- **THEN** the verdict says the daemon cannot ask, which is distinct from a tag that is gone

### Requirement: Answers are cached, aged and asked again only on request
An answer SHALL be kept for the session with the time it was obtained, keyed by registry host,
repository and tag, or by daemon, repository and tag for a fallback answer. The tab SHALL show
the age of the answer, and there SHALL be no background polling.

#### Scenario: Reopening the tab
- **WHEN** the tab is reopened within the session after a check
- **THEN** the kept answer is drawn with its age and no request is sent

#### Scenario: Asking again
- **WHEN** the operator asks for the check again
- **THEN** the answer is replaced and its age restarts

### Requirement: A batch is sequential, cancellable and yields to quota
A batch SHALL check eligible images one at a time as a cancellable job, SHALL skip images whose
verdict is not applicable, and SHALL stop when a registry refuses for quota and report when the
quota resets. It SHALL populate one list column that is dropped before any column the CLI shows.

#### Scenario: The quota runs out mid-batch
- **WHEN** a registry answers a quota refusal during a batch
- **THEN** the batch stops, keeps the answers already obtained, and reports the reset time

#### Scenario: Cancelled midway
- **WHEN** the operator cancels a running batch
- **THEN** it stops before the next request and the answers already obtained remain

### Requirement: Registry egress is named, switchable and bounded
Before asking, the tab SHALL name the host it will contact and whether a credential is involved.
`--no-registry` SHALL forbid every registry request and SHALL say so on the tab. Responses SHALL
be size-bounded, SHALL be treated as untrusted text, and SHALL lose the credential when a
request is redirected to another host. Plain HTTP SHALL be used for loopback hosts only.

#### Scenario: Air-gapped use
- **WHEN** the desk runs with --no-registry and the Upstream tab is opened
- **THEN** no request leaves the machine and the tab says registry access is switched off

#### Scenario: A redirect to another host
- **WHEN** a registry redirects a request to a different host
- **THEN** the credential is not sent to that host

#### Scenario: A hostile answer
- **WHEN** a registry response contains control characters or an oversized body
- **THEN** the text is sanitised before display and the body is cut off at the limit

### Requirement: Trust is stated, not extended
The desk-side client SHALL verify TLS with the system roots and SHALL refuse to fall back to
plain HTTP for a non-loopback host. It SHALL NOT add a private certificate authority in this
version, and the tab SHALL say when a registry could not be verified rather than trusting it.

#### Scenario: A private certificate authority
- **WHEN** a registry presents a certificate the system does not trust
- **THEN** the desk-side verdict is unknown with the verification failure, and the daemon
  fallback may still answer moved or current
