## Purpose

Defines the Images › Findings tab: how an image's archive is obtained and read, what a finding
is, which rules exist, how findings are joined with live state, and how secrets are handled.

## ADDED Requirements

### Requirement: Reading the archive
Opening the Findings tab on an image SHALL obtain the image's archive with a GET request for
that image ID and its platform, and SHALL read it as a stream to its end without writing to
disk, with every buffer bounded. Layers SHALL be ordered by `manifest.json`; `index.json` SHALL
be used for attestations and for what `manifest.json` omits. A blob that a nested index
references but the archive lacks SHALL be skipped without failing the analysis.

#### Scenario: Both layouts
- **WHEN** the archive is a classic docker-archive or an OCI image layout
- **THEN** the same findings are reported, and each layer is checked against its diff ID

#### Scenario: Absent blobs
- **WHEN** the nested index lists another platform and attestation layers that are not in the archive
- **THEN** the analysis completes and notes what was skipped

### Requirement: A finding
Every finding SHALL carry a rule ID, a source (daemon, image or desk), a certainty (certain,
likely or hint), its layer, path, masked evidence, a one-line reason and what cannot be known,
and SHALL carry the build instruction that made its layer only when the history's
layer-making entries map one to one onto the layers. There SHALL be no severity score.

#### Scenario: Deleted secret
- **WHEN** a layer adds a credential file and a later layer deletes it
- **THEN** a `secret.deleted` finding names the adding layer and says the bytes are still in the image

#### Scenario: History does not map
- **WHEN** the count of layer-making history entries differs from the number of layers
- **THEN** no finding shows an instruction

### Requirement: Live join
The tab SHALL show the containers using the image with their state and published ports, and
SHALL report privileged containers, host networking, ports published on every interface,
containers created from a tag rather than a digest, and tags moved since a container was
created. The live part SHALL be recomputed whenever the pane is drawn, without re-reading the
archive.

#### Scenario: State changes
- **WHEN** a container using the image stops
- **THEN** the tab shows it stopped and no new archive request is made

### Requirement: Secrets
Secret values SHALL be masked in the list and the detail. `v` SHALL reveal the value of the
highlighted finding only, and the value SHALL be hidden again when the cursor moves. A secret
value SHALL NOT appear in a job record, an error, a file or a log.

#### Scenario: One at a time
- **WHEN** the operator reveals a secret and moves the cursor
- **THEN** the value is no longer on screen

### Requirement: Jobs, cache and consent
The analysis SHALL run as a cancellable job reporting bytes read and layers found. Its report
SHALL be kept by image ID and platform, and opening the tab again on the same image SHALL draw
it without a request. On a remote daemon, an image larger than the threshold SHALL NOT be
requested until the operator presses Enter.

#### Scenario: Second open
- **WHEN** the tab is opened again on an image already analysed
- **THEN** the findings are drawn in the same frame and no request is sent

#### Scenario: Read-only mode
- **WHEN** the desk is read-only
- **THEN** the analysis completes and no mutating request is sent

### Requirement: Ordering
The findings SHALL be ordered by layer, and `o` SHALL cycle the order to group and to certainty.

#### Scenario: Cycle
- **WHEN** the operator presses `o` three times
- **THEN** the order goes group, certainty, layer
