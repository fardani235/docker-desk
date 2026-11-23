# Spec Delta

## MODIFIED Requirements

### Requirement: Its connections and storage are carried
The plan SHALL reproduce published ports with their host addresses and host ports, ports that
are exposed without being published, bind and volume mounts with their read-only flag, every
user-defined network the container is attached to with its aliases, and the container's network
mode. An anonymous volume SHALL be reused by the name the daemon gave it, never replaced by a
new empty one.

#### Scenario: A fixed host port
- **WHEN** the container publishes `8080:80`
- **THEN** the new container publishes the same host address, host port and protocol

#### Scenario: An exposed-only port
- **WHEN** a port is exposed in the image or config but not published
- **THEN** the new container exposes it and does not publish it

#### Scenario: An anonymous volume
- **WHEN** the container mounts an anonymous volume the daemon named
- **THEN** the new container mounts that same volume, so its data is not lost

#### Scenario: A read-only bind
- **WHEN** the container mounts a host path read-only
- **THEN** the new container mounts the same path read-only

#### Scenario: A named volume in the daemon's default mode
- **WHEN** the container mounts a named volume the daemon reports with its default mode, without an explicit mode on the mount
- **THEN** the new container mounts the same volume under the same destination and the re-create is offered

#### Scenario: Several networks
- **WHEN** the container is attached to the compose project's network with an alias
- **THEN** the new container is attached to the same network with the same alias

### Requirement: A setting outside the model refuses the action
When the container carries a setting that changes what it may do to the host or how it is
isolated, or a fixed network address, and the plan does not reproduce it, the desk SHALL refuse
the re-create and name every such setting. No confirmation SHALL be accepted and nothing SHALL
be sent.

#### Scenario: A privileged container
- **WHEN** the container runs privileged
- **THEN** the dialog lists privilege among the settings it cannot reproduce and offers no way to proceed

#### Scenario: A custom runtime
- **WHEN** the container was created with a runtime such as a GPU runtime
- **THEN** the dialog names that runtime and refuses

#### Scenario: A borrowed network namespace
- **WHEN** the container uses another container's network namespace
- **THEN** the dialog names the namespace mode and refuses

#### Scenario: A fixed address
- **WHEN** the container's network attachment has a fixed address configured
- **THEN** the dialog names the address setting and refuses

#### Scenario: A private label on a volume
- **WHEN** a named volume was mounted with the private label `Z`
- **THEN** the dialog names the mount option and refuses

#### Scenario: A label on a bind
- **WHEN** a bind mount was created with the shared label `z` or the private label `Z`
- **THEN** the dialog names the mount option and refuses

#### Scenario: Several unsupported settings
- **WHEN** the container has a capability, a device and a log driver option
- **THEN** the dialog names all three, not only the first
