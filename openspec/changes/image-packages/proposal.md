# Proposal

## Why

Images › Findings reads an image's SBOM and reduces it to one line holding a count. An SBOM is
a claim about what an image carries; the image's own package databases are the evidence. Whether
the two agree — what is installed and not declared, what is declared and not installed, what is
installed at another version — is not shown anywhere, and an SBOM above the 4 MiB JSON bound is
not read at all.

## What Changes

- Read every SBOM's packages (name, version, licence, purl) as the archive streams, with bounded
  memory, so a document of any size is browsable; keep a bounded table and the true total.
- Read the installed databases during the layer walk — dpkg's `status` and `status.d/*`, apk's
  `installed`, rpm's `rpmdb.sqlite` under `/var/lib/rpm` or `/usr/lib/sysimage/rpm` — into
  records, dropping the bytes. rpm's database is read by a new read-only SQLite reader and rpm
  header parser, with no new dependency.
- Decide which copy of a database counts with the existing final-filesystem logic, and attribute
  each package to the layer that introduced it by diffing the database across layers.
- Join the two sides within one ecosystem at a time; every other ecosystem is not checkable.
- Add two supply findings: one naming the undeclared OS packages, one hint for declared OS
  packages that are not installed or installed at another version.
- Add a full-screen Packages view, opened with `i` from Findings, with search, ordering, a state
  filter, and a header that states both sides and every unknown.

## Capabilities

### New Capabilities

- `image-packages`: the declared and installed package lists of an image, their join, the
  findings drawn from it, and the view that browses it.

### Modified Capabilities

- None in `openspec/specs/`. The `image-findings` change (not yet archived) gains two supply
  rules and its SBOM summary now counts documents above the JSON bound; its requirements still
  hold as written.

## Impact

- `internal/forensics`: new `sbom.go`, `sqlite.go`, `rpmdb.go`, `pkgdb.go`, `installed.go`,
  `packages.go`, `pkgrules.go`; the JSON blob path and the layer walk feed them; `finalFS`
  treats a directory replaced by a symbolic link as hiding what was under it; `sanitize` also
  replaces bidirectional controls.
- `internal/ui`: new `packages.go`; `i` in Findings; keys, hints and help.
- `internal/forensics/fixture`: package databases, SPDX and CycloneDX documents, zstd-marked
  layers, and the demo image the fake daemon serves for `python:3.12-slim`.
- Tests and fixtures: a real `rpmdb.sqlite` from ubi9-micro, with what rpm reports for it.
- No new request, no write, no new dependency; read-only mode is unchanged.
