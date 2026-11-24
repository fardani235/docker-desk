# Design

## Context

Space already reads and renders the build cache's records from `GET /system/df`
(`internal/ui/space.go`), but offers no action for them: `spaceHints` lists only `d` and `X`,
and the README says the cache is never removed.

Three constraints determine the approach:

- **Docker has no per-record build-cache delete.** The only mutation is `POST /build/prune`.
  Its `all` flag means "include internal/frontend references" (`docker buildx prune -a`), not
  "include in-use records"; Docker never prunes a record a build is using. The filter set
  accepts only one `id` value and matches it as a prefix (`id~=` in moby's builder), so the
  desk cannot pin a multi-record set the way it pins image and volume prunes.
- **The current plan model sends one request per step.** `plan.Execute` iterates `Steps`, and
  the confirmation's "exactly these N" list is built from those steps. A single build-cache
  request has no one-step-per-object mapping, and claiming one would be false.
- **The existing prune scope switch re-reads lists.** `dialog.scope` is
  `func(plan.Snapshot, bool) Plan`; build-cache eligibility lives in `/system/df`, which is not
  part of `plan.Take` and is expensive enough that it is normally read as a job.

See `proposal.md` for motivation and the two spec deltas for required behavior.

## Goals / Non-Goals

**Goals:**

- Reclaim the build cache from Space with the same deliberate consent as every other mutation.
- Show the operator the candidate records, their sizes, the account's age, and which records
  are in use and therefore kept.
- Say plainly, in the confirmation and in the job log, that the daemon re-evaluates
  eligibility and that the set is not pinned.
- Keep the exact-list promise for image, volume and network prunes intact.

**Non-Goals:**

- Pinning the build-cache set by ID, `until`, or size thresholds.
- Removing individual cache records.
- A build-cache view separate from Space, or a builder.
- Changing the prune behavior of any other kind.

## Decisions

### D1 — One new engine method, and it is the only set-acting mutation

```go
// BuildCachePrune asks the daemon to remove unused build-cache records
// (POST /build/prune). all includes internal/frontend records. The daemon
// chooses the records; the report names what it removed. This is the only
// mutating method whose request acts on a set.
BuildCachePrune(ctx context.Context, all bool) (BuildCachePruneReport, error)
```

`BuildCachePruneReport` carries `Deleted []string` and `Reclaimed int64`, mapped from the
daemon's `CachePruneReport`. The moby call is `BuildCachePrune` with only `All` set: no
filters, no reserved/max/min space, no force. The read-only wrapper returns `ErrReadOnly`
before anything is sent. The engine interface's comment ("Every mutating method takes the
daemon identifier of exactly one object") is amended to name this exception.

**Alternatives rejected.** One request per cache record is impossible (no endpoint). A prune
filtered by `id` cannot list a set (one value, prefix match), and inventing an `until` window
would still not match what was shown. A size threshold (`reserved-space`) would be a different
feature.

### D2 — The plan has one step and a separate, display-only preview

`plan.BuildCachePrune(du engine.DiskUsage, all bool) Plan` builds:

- one `Step{Op: OpPruneBuildCache, Kind: "build cache", Name: scope, Size: 0}` — the single
  request `Execute` sends;
- `Plan.BuildCache` holding the account's records, the candidate set, the in-use count, the
  account time, whether it was derived, and the daemon's reclaimable figure — the preview the
  dialog renders, never executed;
- the one step as the only request.

`Plan.Listed` keeps its meaning for object prunes. `planBody` gains a branch: when
`p.BuildCache != nil`, it renders the candidate records through the same scrolling list path
but with the headline "The daemon will remove unused build-cache records; at the last account
these were:" and a note that the daemon decides the final set. The typed word is `prune`, the
title is "Prune build cache", and the tier is `TierTyped`.

`Execute` gains `case OpPruneBuildCache`: call the engine method, log the daemon's count,
reclaimed bytes and (bounded) IDs, compare the report to the preview's IDs, log "the daemon
also removed …" for records the preview did not list, and log "… was kept" for listed records
that are absent. `Step.Size` stays 0 so the generic "reclaimed about" line cannot claim the
preview's figure; the daemon's `Reclaimed` is the only number reported.

**Alternative rejected.** A dedicated build-cache dialog outside `Plan` would bypass the
typed confirmation, arming, plan sequence and job ledger. A plan with one step per record would
send one prune per record.

### D3 — The preview reads a fresh account, and Tab reads another

`m.prepareBuildCachePrune(all bool)` follows `prepare`'s shape (plan sequence, `preparing`
status, a command tagged with the focus generation) but calls `eng.DiskUsage` with a 30 s
bound instead of `plan.Take`. It answers with a `planMsg` carrying the built plan and a
`rebuild func(bool) tea.Cmd` closure.

The dialog gets `rebuild` alongside `scope`. On `Tab`, if `rebuild` is set, the desk sets
`recomputing`, clears the typed word, and calls `rebuild(!scopeAll)`; the new preview replaces
the dialog exactly as an object-prune scope switch does, so the arming delay starts again and
the confirmation text is empty. Any key pressed before the answer drops the request through
the existing `planSeq` rule.

**Alternative rejected.** Reusing `Space`'s cached account would be instant but could confirm
a list taken minutes ago; the whole point of this dialog is that it shows what the daemon sees
now. Reusing `preparePrune` would fetch four lists the prune does not need.

### D4 — Candidates are what the daemon can remove, and the figures are the daemon's

- Default scope: `!InUse` and type not `internal` or `frontend`.
- All-types scope: `!InUse` (every type).
- In-use records are never candidates; the dialog says how many are kept.
- Records keep their `Shared` flag. Sizes are listed per record, but no total is presented as
  bytes that will certainly be freed: shared cache bytes are not attributed by the account.
  The "frees" line quotes the daemon's `Reclaimable` for the scope and says it is the daemon's
  estimate, with the account's age and a `≥`-style caveat for shared/internal bytes.
- The candidate list is sorted largest first and scrolls; no artificial cap beyond what the
  account already holds.

### D5 — The confirmation states the daemon's discretion, and the job reports the truth

The dialog carries two warnings:

1. the daemon re-checks eligibility at request time: cache created or released after the
   account may also be removed, and a record that became in use may be kept;
2. the preview is a snapshot taken at a stated time, not a pinned set.

After the job, the desk:

- logs the daemon's `Deleted` count/IDs and `Reclaimed` bytes as the outcome;
- names any deleted record that was not in the preview and any previewed record the daemon
  kept;
- marks `sp.duAt` stale immediately and, when the Space view is open, starts a fresh account
  read once the prune job finishes (tracked by the job's ID in `spaceState`, checked from
  `collectSpace`), so the totals and records reflect the prune without a key.

This is the honest form of "a prune shows exactly what it will destroy": for the build cache,
the dialog shows the candidates and the daemon's rule, and the result claims only the daemon's
report.

### D6 — Read-only is enforced twice

`spaceHints` omits `b`, `onSpaceKey` does not open the confirmation, and the engine's
`readOnlyEngine.BuildCachePrune` returns `ErrReadOnly` before sending. A bug in one layer
cannot send a prune.

### D7 — The fake daemon serves the endpoint

`internal/fakeengine` gains `POST /build/prune`: it reads `all`, removes every record that is
not `InUse` and — only with `all` — also the `internal`/`frontend` types, sums the removed
sizes, and answers `{"CachesDeleted":[…],"SpaceReclaimed":…}`. The request is recorded, so
tests can assert "one POST, no force, nothing else". Seeding can add an in-use and an internal
record to exercise both scopes. The cross-check runs the real `docker builder prune` against
the fake so the endpoint is validated by Docker's own client.

## Risks / Trade-offs

- **The daemon removes a record the preview did not list** → Disclosed in the dialog and named
  in the job; no force, unused-only scope, and a typed word. This is the accepted trade-off of
  using the only endpoint Docker offers.
- **A record becomes in use between the account and the request** → The daemon keeps it; the
  job says it was not removed. The reverse (unused after the account) may be removed and is
  disclosed.
- **Shared bytes are understated** → No total is claimed as certainly freed; per-record sizes
  carry the shared flag and the frees line quotes the daemon's estimate.
- **`/system/df` is slow on a busy engine** → The read is bounded at 30 s, shows the existing
  "reading the daemon's current state" status, and any key drops interest through `planSeq`.
- **An old daemon does not serve `/build/prune`** → The job fails with the daemon's message and
  no account figure is claimed as reclaimed.
- **The fake and the real daemon diverge on `all`** → The fake mirrors the documented meaning
  (internal/frontend, never in-use), and the cross-check runs the real CLI's prune against the
  fake, so the request shape and report are Docker's own client's, not the desk's guess.

## Migration Plan

None. The action is additive; `--read-only` keeps it hidden and refused. Reverting removes the
key, the engine method and the fake endpoint. The Space screen captures change only in their
hints; no stored data or API shape changes.
