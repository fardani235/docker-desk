## Context

Images had two tabs, Info and Inspect, both drawn from `GET /images/{id}/json`. Read-only mode
permits only GET and HEAD, enforced twice (the `ReadOnly` wrapper and the client's request
hook). The desk's rules: live data never arrives as messages; every slow operation is a job;
nothing is guessed.

## Goals / Non-Goals

**Goals:** report what is in an image's bytes, with evidence, source and certainty; join it
with live state; keep every secret off everything but the screen; stream with bounded memory;
work unchanged in read-only mode and on remote daemons.

**Non-Goals:** vulnerability matching, signature verification of attestations, zstd layers,
Windows images, writing anything anywhere.

## Decisions

The full reasoning is in `docs/DECISIONS.md`, F1–F14. In short:

- **Transport (F1, F2).** The library's `ImageSave` — `GET /images/get?names=` — same daemon
  handler as `/images/{id}/get`, inside D3. A read method of `Engine`.
- **Streaming (F3, F4).** Entries are classified by their first bytes because `manifest.json`
  may come last; layers are walked as they pass and reduced to fingerprints, whiteouts,
  opaque markers and observations; order is applied at the end. `Limits` bound everything; a
  bound that cuts something off is noted.
- **Manifest and index (F5).** `manifest.json` first; `index.json` for attestations, absent
  platforms, and as a fallback when there is no `manifest.json`.
- **Findings (F6–F8).** Source and certainty defined; instructions only by exact count;
  secrets and caches reported across layers (deleted, replaced); modes and binaries only for
  the final filesystem.
- **Secrets (F9).** `forensics.Secret` masks itself in every format; errors are the desk's own.
- **Jobs and cache (F10, F11).** One analysis at a time; LRU of eight reports by ID and
  platform; consent above 500 MB on a remote daemon.
- **Live join (F12).** List data plus inspect for at most 20 containers, refetched when the
  list changes.

## Risks / Trade-offs

- A revealed secret is on screen (and in scrollback) until the next frame replaces it.
- Heuristic rules (generic assignments, canary symbols) are labelled `likely` or `hint`, with
  an unknown; they can still be wrong, and say how.
- Large JSON blobs (an SBOM over 4 MiB) are noted as present but not read.
