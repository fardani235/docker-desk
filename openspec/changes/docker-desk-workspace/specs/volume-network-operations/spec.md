## Purpose

Defines the volume and network actions available from the desk: create, remove and prune for
both, and connecting and disconnecting containers for networks.

## ADDED Requirements

### Requirement: Create a volume
The operator SHALL be able to create a volume by typing a name. It SHALL be created with the
default local driver.

#### Scenario: Creating
- **WHEN** the operator opens the create form in the Volumes kind, types a name and presses Enter
- **THEN** the volume is created and appears in the list

#### Scenario: Invalid or empty name
- **WHEN** the name is empty or contains characters Docker does not accept in a volume name
- **THEN** nothing is sent and the form says what is wrong

#### Scenario: Name already exists
- **WHEN** a volume with that name already exists
- **THEN** nothing is sent and the form says the name is taken

### Requirement: Remove a volume
The operator SHALL be able to remove the highlighted volume following the mutation-safety
rules. The confirmation SHALL state that the volume's data will be permanently deleted.

#### Scenario: Unused volume
- **WHEN** the operator removes a volume no container references and completes the typed confirmation
- **THEN** the volume is removed without force and disappears from the list

#### Scenario: Volume in use
- **WHEN** any container references the volume
- **THEN** removal is refused and the containers are named

### Requirement: Prune unused volumes
The operator SHALL be able to prune volumes, choosing between anonymous unused volumes only
(the default) and all unused volumes including named ones. The prune SHALL follow the exact-set
prune rule.

#### Scenario: Default scope
- **WHEN** the operator opens volume prune
- **THEN** the preview lists only anonymous volumes that no container references

#### Scenario: Including named volumes
- **WHEN** the operator switches the preview to all unused volumes
- **THEN** named volumes that no container references are listed as well, and the dialog warns that named volumes usually hold data that was kept on purpose

### Requirement: Create a network
The operator SHALL be able to create a network by typing a name. It SHALL be created with the
`bridge` driver and engine-assigned addressing.

#### Scenario: Creating
- **WHEN** the operator opens the create form in the Networks kind, types a name and presses Enter
- **THEN** the network is created and appears in the list

#### Scenario: Name already exists
- **WHEN** a network with that name already exists
- **THEN** nothing is sent and the form says the name is taken

### Requirement: Remove a network
The operator SHALL be able to remove the highlighted network following the mutation-safety
rules.

#### Scenario: Unused custom network
- **WHEN** the operator removes a custom network with no containers and completes the typed confirmation
- **THEN** the network is removed and disappears from the list

#### Scenario: Network with containers or predefined network
- **WHEN** containers use the network, or it is `bridge`, `host` or `none`
- **THEN** removal is refused with the reason

### Requirement: Prune unused networks
The operator SHALL be able to prune custom networks that no container, running or stopped, is
attached to or configured to use. Predefined networks SHALL never be listed. The prune SHALL
follow the exact-set prune rule.

#### Scenario: Preview
- **WHEN** the operator opens network prune
- **THEN** the preview lists only custom networks that no container references

### Requirement: Connect and disconnect containers
The operator SHALL be able to connect a container to the highlighted network and to disconnect
an attached container from it. The operator SHALL pick the container from a list, and the
action SHALL then be confirmed following the mutation-safety rules. Disconnection SHALL NOT be
forced.

#### Scenario: Connecting
- **WHEN** the operator chooses connect on a network, picks a container that is not attached and confirms
- **THEN** the container is attached to the network and appears among the network's containers

#### Scenario: Only eligible containers are offered
- **WHEN** the connect picker is shown
- **THEN** it lists only containers that are not already attached to that network

#### Scenario: Disconnecting
- **WHEN** the operator chooses disconnect on a network, picks an attached container and confirms
- **THEN** the container is detached from the network

#### Scenario: Nothing to disconnect
- **WHEN** the operator chooses disconnect on a network with no attached containers
- **THEN** the desk says there is nothing to disconnect and opens no picker

#### Scenario: Network does not allow it
- **WHEN** the operator chooses connect or disconnect on the `host` or `none` network
- **THEN** the desk says containers cannot be connected to or disconnected from that network
