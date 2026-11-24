# Proposal

## Why

Space accounts for the build cache and shows what the daemon calls reclaimable, but the desk
cannot reclaim it: `b` does not exist, and the README says outright that the cache is "never
removed". On a machine used for builds that cache is often the largest reclaimable figure on
the screen, so the one view that explains where the bytes went cannot free them.

## What Changes

- Space gains `b`: a typed confirmation (`prune`) that reclaims the build cache. It is hidden
  in read-only mode.
- The confirmation is built from a fresh `GET /system/df`: every candidate record is listed
  with its type, description, size and flags, the daemon's account age is shown, and in-use
  records are never offered. `Tab` switches scope: unused records (the default, excluding
  internal/frontend types) and all unused records including those types, mirroring
  `docker builder prune` and `-a`.
- Confirming sends the daemon's own `POST /build/prune` with that scope. This is the only
  request sent, and no force is ever used.
- The dialog says plainly that the daemon re-evaluates eligibility: it may also remove cache
  created or released after the account, and may keep a record that became in use. The job
  logs the daemon's report (records deleted, bytes reclaimed), names any difference from the
  preview, and the Space account is marked stale.
- The engine gains one set-acting mutation (`BuildCachePrune`), the fake engine gains the
  endpoint, and the read-only wrapper refuses it.
- Existing image, volume and network prunes keep their exact-list promise; the specs separate
  that promise from the build cache's daemon-chosen set.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `disk-space`: reclaim now includes a build-cache action with its own preview, scope,
  confirmation and result reporting; the old "never calls a prune endpoint" rule is replaced
  for this one action.
- `mutation-safety`: the exact-list prune rule is scoped to images, volumes and networks, and a
  new rule covers the build cache: candidates are listed, the daemon's discretion is stated,
  in-use records are never offered, and the report is what the job claims.

## Impact

- `internal/engine`: the interface, the moby implementation, the read-only wrapper, the disk
  types, and `internal/fakeengine`'s `POST /build/prune` plus its request log.
- `internal/plan`: a new `OpPruneBuildCache` step, a build-cache plan builder, and a
  display-only preview list for records (the existing `Listed` path assumes one object per
  request).
- `internal/ui`: Space's `b` key and hints, a build-cache preparation path that reads
  `/system/df` (not the list snapshot), the dialog's scope switch and preview rendering, and
  the stale-account/refresh handling after the job.
- `README.md`, `docs/DECISIONS.md`, `docs/TESTING.md` and the Space screen captures.
- Tests: engine/fake request and report, plan preview and execution, UI confirmation and
  read-only behavior, and an end-to-end pty run.
- No new dependency; the moby client already exposes `BuildCachePrune`.
