## Context

The analyser (`internal/forensics`) reads the archive of `GET /images/get` once, as a stream:
the outer tar's entries are classified by their first bytes (manifest.json may come last), each
layer is walked as it passes, and cross-layer rules run in `finish` over per-layer summaries.
JSON blobs up to 4 MiB are kept whole; larger ones are only counted. `finalFS` answers whether a
path's copy in a layer survives the layers above it.

## Goals / Non-Goals

Goals: both package lists, read in the same single pass; an honest join; every bound stated.

Non-goals: vulnerability matching; reading Berkeley DB or NDB rpm databases; reading zstd layers;
SBOM formats other than SPDX 2.x and CycloneDX JSON; language package databases (npm, pip, Go
modules) as an installed side.

## Decisions

The decisions are recorded in full in `docs/DECISIONS.md`, round seven (K1–K15). In short:

- **Streaming SBOM scan (K1).** Every JSON blob passes through a small JSON lexer with bounded
  strings (2 KiB kept per string) and nesting (64), tee'd into the whole-document buffer when
  the document fits the old bound. `encoding/json` was not used: it buffers a whole token, so one
  long string in a large SBOM would defeat the bound. A document that fails to read offers no
  partial list.
- **rpm without a dependency (K2–K3).** A SQLite reader that walks only table b-trees from the
  file in memory: header, `sqlite_master` on page 1, interior and leaf table pages, overflow
  chains, the record format. Each page is visited at most once; any inconsistency fails the
  read. A non-empty `-wal` or `-journal` beside the database makes rpm's list unknown.
- **Which copy counts (K4).** For each layer j, the database "as of j" is the topmost copy of
  each path at or below j that `finalFS` (restricted to layers ≤ j) says is still visible.
- **Layer attribution (K5).** The layer of a package is the first of the unbroken run of states,
  ending at the last layer, that lists its name, version and architecture. A state that cannot be
  known breaks the run and the attribution is marked unsure, with the reason.
- **Join (K6–K7).** Within an ecosystem, by name: the same version first, then the same name at
  another version. Epochs that only one side states, or that are 0, do not make versions differ.
  gpg-pubkey entries are not packages.
- **View (K8–K12).** Space's layout; header lines scroll with the list, a fixed line says the
  order, state filter and search; the cursor follows the package by identity and keeps it while a
  filter hides it.

## Risks / Trade-offs

- A malformed SQLite file could still decode to a wrong but whole list if its damage is
  self-consistent; the reader refuses every inconsistency it can see, and is fuzzed.
- Every JSON blob is now scanned, not only SBOMs: measured as negligible next to the layers.
- Database copies across layers are kept as records until the end (bounded at 200,000 records),
  because which copy is final is known only once the manifest has been read.
