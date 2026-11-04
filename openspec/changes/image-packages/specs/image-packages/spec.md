## Purpose

Defines the image's package lists: what its SBOM declares, what its own package databases say is
installed, how the two are joined, the findings drawn from the join, and the view that browses
it.

## ADDED Requirements

### Requirement: Declared packages are read as the archive streams
The analysis SHALL read the packages an SBOM declares — name, version, licence and purl — from
SPDX `packages[]` and CycloneDX `components[]` (nested components included), at the top level
of the document or under an in-toto statement's `predicate`, while the blob streams, with
bounded memory. It SHALL keep at most 20,000 packages of one SBOM and SHALL keep the true total.
A document that cannot be read to its end SHALL yield no list and the reason.

#### Scenario: An SBOM above the JSON bound
- **WHEN** the SBOM document is larger than the 4 MiB bound for JSON documents kept whole
- **THEN** its packages are listed and counted as for a small one

#### Scenario: More packages than are kept
- **WHEN** the SBOM lists more packages than the bound
- **THEN** the view says how many are kept of how many, and no installed package is called
  undeclared on the strength of the capped table

### Requirement: Installed packages are read in the layer walk
The analysis SHALL parse every copy of `/var/lib/dpkg/status`, `/var/lib/dpkg/status.d/*`,
`/lib/apk/db/installed` and rpm's `rpmdb.sqlite` (under `/var/lib/rpm` or
`/usr/lib/sysimage/rpm`) into records as each layer streams, and drop the bytes. It SHALL read
at most 64 MiB of an rpm database and 8 MiB of a text database, and keep at most 10,000
packages of one database.

#### Scenario: A database above its bound
- **WHEN** a dpkg status file is larger than 8 MiB
- **THEN** what dpkg has installed is unknown, and the reason names the bound

### Requirement: rpm's database is read without a dependency
rpm's `rpmdb.sqlite` SHALL be read by the desk's own read-only reader, enumerating only the
Packages table, and each header SHALL yield tags 1000 to 1003, 1014 and 1022. A database that
cannot be read whole SHALL yield no list.

#### Scenario: A real rpm database
- **WHEN** the database is the committed `rpmdb.sqlite` taken from ubi9-micro
- **THEN** the packages read are exactly those rpm itself reports for it

#### Scenario: A database that cannot be read
- **WHEN** a database is damaged, in Berkeley DB or NDB format, or has a non-empty write-ahead
  log or journal
- **THEN** what that manager has installed is unknown, with the reason, and no partial list is
  shown

### Requirement: The final filesystem decides which copy counts
Which copy of a database counts SHALL be decided with the final-filesystem logic: whiteouts,
opaque directories, directories replaced by links, and later layers that cannot be read. A
database deleted in a later layer SHALL make the installed list unknown, not empty. A layer that
cannot be read SHALL make the installed list unknown unless a readable copy above it decides.

#### Scenario: A zstd image
- **WHEN** the image's layers are zstd-compressed
- **THEN** the installed side is unknown with the reason, and the SBOM's packages are still
  listed, each as not checkable

### Requirement: Layer attribution
Each installed package SHALL be attributed to the layer that introduced its name and version, by
diffing the database across layers; where a copy in between could not be read, the attribution
SHALL be marked unsure with the reason. The build instruction SHALL be shown where the history
maps onto the layers.

#### Scenario: A package installed in a later layer
- **WHEN** a package first appears in the fourth layer's copy of the database
- **THEN** its layer is 4 and the view shows that layer's instruction

### Requirement: The join is within an ecosystem
The two sides SHALL be joined when the view is drawn, within one ecosystem only: `pkg:deb`
against dpkg, `pkg:apk` against apk, `pkg:rpm` against rpm. Every row SHALL be one of: both,
version differs, declared only, undeclared, not checkable. A package of any other ecosystem, a
package without a purl, and every package whose other side is unknown SHALL be not checkable,
with the reason, and never undeclared.

#### Scenario: Another ecosystem
- **WHEN** the SBOM declares a `pkg:pypi` or `pkg:golang` package
- **THEN** its row is not checkable

### Requirement: Findings from the join
An installed OS package absent from the SBOM SHALL be reported in one supply finding that names
the packages and says what cannot be known about why. Declared-only and version-differs OS
packages SHALL be counted in the view's header and reported in one hint finding.

#### Scenario: Undeclared packages
- **WHEN** three packages installed in layer 4 are not in the SBOM
- **THEN** one `supply.undeclared` finding names them, with layer 4 and its instruction

### Requirement: The Packages view
`i` on the Findings tab SHALL open a full-screen view of the report's packages once the analysis
has finished, and `Esc` SHALL close it onto the desk exactly as it was. The view SHALL send no
request, and SHALL state the SBOM's format and count, the database read and its count, and every
unknown with its reason.

#### Scenario: Both layouts
- **WHEN** the terminal is 80×24 or 120 columns wide
- **THEN** the view renders and scrolls without tearing the frame

#### Scenario: Read-only mode
- **WHEN** the desk is read-only
- **THEN** the view works unchanged and no mutating request is sent

### Requirement: Package rows
Each row SHALL show name, version, licence, type, layer and state, and the highlighted row's
detail SHALL be shown underneath: both versions, the purl, the file that lists it, and the layer
and instruction that added it.

#### Scenario: An undeclared package
- **WHEN** the cursor rests on a package installed in layer 4 and absent from the SBOM
- **THEN** the detail says it is installed and not declared, and names layer 4's instruction

### Requirement: Searching and ordering packages
`/` SHALL search name, version, licence and purl; `o` SHALL order by name, state, layer or
licence; `s` SHALL cycle the state shown. The cursor SHALL follow the package, not the row index.

#### Scenario: The cursor follows the package
- **WHEN** the order, the search or the state shown changes while a package is highlighted
- **THEN** the same package stays highlighted whenever it is shown
