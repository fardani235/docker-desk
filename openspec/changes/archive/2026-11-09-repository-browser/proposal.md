# Proposal

## Why

Images › Upstream answers one question about one local image: does its tag name another build
today. It cannot answer the question an operator asks next — *which tags does this repository
have, what does each name, and which of them do I already hold?* — and it offers no way to
fetch the build it found except the pull form, which pulls a tag, not the build the tag named
when it was read. Typing a 71-character `name:tag@sha256:…` into that form is what an operator
does today, if they do it at all.

## What Changes

- A full-screen **repository browser**, opened with `B` (after Space in the code), restoring
  focus, kind and highlight on close. On an image it lists that image's repository's tags from
  its registry and asks at once; with no image it opens on an empty name line.
- `registry.ListTags` over `GET /v2/<path>/tags/list?n=100&last=<tag>`, following `Link` on the
  same host and path only; one page on open, `Tab` for the next; at most 1000 held. Tags are
  sorted naturally and labelled as text order, never as recency.
- `registry.Describe`: one manifest GET — digest, media type and an index's platforms — and
  never the child manifest or the config. The highlighted row is described once the highlight
  rests on it, and the answer is kept by host/path:tag with its age.
- A **HERE** column joining the remote digest with the `RepoDigests` of local images of the same
  repository (never an image ID): `here`, `here, the tag moved upstream`, `same build as <tag>`,
  or `-`. A tag whose index has no build for the daemon's architecture says so before any pull.
- `d` reads build times for the rows on screen as one cancellable job shaped like
  `freshness.Batch`: up to three requests a row through `Resolve`, stopping at a quota refusal
  and keeping what was read. Each time names the platform whose config it came from.
- `plan.PullPinned`: a typed confirmation whose word is the tag, pulling exactly
  `name:tag@sha256:<digest>`. The dialog shows the full reference and says the digest, not the
  tag, is fetched. The browser's `Enter` and Images › Upstream's moved verdict (`P`) both open it.
- `/` filters the loaded rows; `e` focuses the name line (every key is text until Enter or Esc).
- A `401` is said as what it is: "the registry answered 401 - it does not have this
  repository, or this credential is not allowed to see it".
- `--no-registry`: the view refuses to open, saying why. Read-only: browsing works; the pull is
  refused by the existing guard.
- `fakeregistry` answers `tags/list` with `n`, `last` and `Link`; `fake-dockerd -registry
  127.0.0.1:<port>` serves it and seeds three local images, so `make sandbox` shows a list and a
  pinned pull.

## Capabilities

### New Capabilities

- `repository-browser`: listing, describing, joining, build times, the view and its refusals.

### Modified Capabilities

- `image-operations`: a pinned pull — one build by its digest, confirmed by typing its tag.

## Impact

- `internal/registry`: `ListTags`, `Describe`, `ParseRepository`, the 401 sentence.
- `internal/repository`: new — natural order, the capped listing, the join, the session cache,
  the build-time batch, the daemon's platform.
- `internal/plan`: `Pinned`, `PinnedRef`, `PullPinned`.
- `internal/ui`: `repository.go`; `B`; `P` on a moved verdict; hints, help; the typed-word input
  sized to the word.
- `internal/fakeregistry`: `tags/list`, `Listen`, `AliasTags`, `Conceal`, `SetLinkBase`.
- `internal/fakeengine`: a digest pull records the digest it pulled; `SeedRepository`.
- `cmd/fake-dockerd`: `-registry`; `Makefile`: `make sandbox` passes it.
- No new dependency. No new mutation: the pinned pull is the existing pull request.
