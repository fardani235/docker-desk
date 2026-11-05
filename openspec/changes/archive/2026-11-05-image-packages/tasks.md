# Tasks

## 1. rpm, first

- [x] 1.1 Read-only SQLite reader for table b-trees (header, schema, interior and leaf pages, overflow chains, records); verify against a real `rpmdb.sqlite` and one SQLite wrote with 512-byte pages.
- [x] 1.2 rpm header parser for tags 1000–1003, 1014, 1022; verify the real fixture yields exactly what `rpm -qa` reports for it.
- [x] 1.3 Damage: truncation, random byte changes, cycles, wrong encodings fail the read whole; fuzz the reader without its recover.

## 2. Both sides in the stream

- [x] 2.1 Bounded streaming JSON scan of every JSON blob for SPDX and CycloneDX packages; verify a document above the JSON bound and documents that fail to read.
- [x] 2.2 dpkg `status`, `status.d/*` and apk `installed` parsers; capture every database copy during the layer walk within the bounds.
- [x] 2.3 Which copy counts, from `finalFS` per layer; deleted, unreadable, WAL, linked and duplicated databases.
- [x] 2.4 Layer attribution by diffing the database across layers, unsure where a copy could not be read.

## 3. Join and findings

- [x] 3.1 Join within an ecosystem; the five states; epochs; gpg-pubkey; caps.
- [x] 3.2 `supply.undeclared` and `supply.sbom-drift`; the SBOM summary says what it was checked against.

## 4. The view

- [x] 4.1 Packages view from `i` in Findings; header with both sides and every unknown; rows and detail; Esc restores the desk.
- [x] 4.2 `/` search, `o` order, `s` state filter; the cursor follows the package.
- [x] 4.3 UI tests at 80×24 and 120 columns, read-only, zstd, large SBOM; e2e scenario and screen captures.

## 5. Documentation and verification

- [x] 5.1 README, DECISIONS (round seven), JOURNAL, TESTING, help.
- [x] 5.2 `make test`, `make race`, strict OpenSpec validation.
