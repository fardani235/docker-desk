# Spec Delta

## MODIFIED Requirements

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
