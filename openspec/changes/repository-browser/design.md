## Context

Round eleven made the desk a registry client (`internal/registry`: references, credentials,
challenges, `Resolve`) with one question, asked of one local image (`internal/freshness`). The
registry API answers two more questions cheaply: which tags a repository lists
(`GET /v2/<name>/tags/list`, paged by `n`, `last` and `Link`), and what one tag names (one
manifest `GET`). The fake registry served manifests and blobs but not `tags/list`. The pull
form pulls what the operator types; `plan.NormalizeRef` leaves a reference carrying a tag and a
digest unchanged and `engine.PullImage` sends it verbatim.

## Goals / Non-Goals

Goals: browse one repository's tags from the operator's own network with the operator's own
credentials; say what each tag names and whether that build is already here, by digest; read
build times only when asked; pull one build by its digest, typed with its tag.

Non-goals: search, `_catalog`, Docker Hub's web API (`hub.docker.com`), ordering by date,
deleting or pushing tags, retagging locally, several repositories at once, background polling.

## Decisions

Recorded in full in `docs/DECISIONS.md`, round twelve (L1–L16). In short:

- **L1 — Its own capability and package.** `internal/repository` holds the browser's knowledge
  (listing, join, cache, batch); the wire stays in `internal/registry`; the view is
  `internal/ui/repository.go`. The pinned pull is a delta on `image-operations`.
- **L2 — Paging follows `Link`, on the same host and path only.** `n=100`; the next page is the
  one the registry names; a `Link` to another host or path is not followed and the view says so;
  a registry that names no next page has none. Names outside the tag grammar are dropped and
  counted, never drawn.
- **L3 — 1000 held.** Ten pages at most. A page that would pass the cap is cut and the view says
  the registry has more.
- **L4 — Natural text order, never recency.** Digit runs by value, the rest bytewise; the header
  says so. Pages arrive in the registry's own (lexical) order and the header says that too when
  more remain.
- **L5 — Describe is one GET, when the highlight rests.** After the desk's select delay; a
  description still in flight for a row the highlight has left is cancelled. Kept by
  host/path:tag with its age. Listing and describing are single bounded requests shown in the
  view, not jobs; `d`, which can take many, is a job.
- **L6 — A 401 never claims absence.** The browser's requests say "the registry answered 401 -
  it does not have this repository, or this credential is not allowed to see it". A 404 says
  the registry has no such repository, because that is what it said.
- **L7 — The join is by digest.** `RepoDigests` of local images of the same repository
  (`Ref.SameRepository`); never an image ID. Precedence: `here` (the digest, under this tag or
  no tag of the repository); `here, the tag moved upstream` (this tag, another recorded digest);
  `same build as <tag>` (the digest, under another tag); `-`. An image built here joins nothing.
- **L8 — The daemon's platform is mapped, not guessed.** `engine.Info.Architecture` is the
  kernel's name (`x86_64`); it is mapped to the index's (`amd64`). An index without it says so on
  the row, and the pinned pull is refused. A single manifest, or an unknown daemon, claims nothing.
- **L9 — `d` is the rows on screen, as one job.** `Resolve` with the daemon's platform: up to
  three requests a row; a quota refusal stops it and keeps what was read; another failure is
  that row's answer. Rows already timed are skipped. Each time names its platform.
- **L10 — One pinned pull.** `plan.PullPinned`, `TierTyped`, `Word` = the tag. The full
  reference is in the effects (wrapped, never cut). The browser's `Enter` and Upstream's `P` on a
  moved verdict call the same action, through the guarded path, so read-only refuses it there.
- **L11 — The premise that the pull tags the image did not hold.** The vendored moby client
  sends the digest in place of the tag when a reference carries both; the daemon records
  `name@sha256:…`. The dialog says so; no second request tags it.
- **L12 — Keys.** `B` beside `S` and `U` (a read, not a guarded action); `e` name line, `/`
  filter, `Tab` next page, `d`, `c`, `r` forget and ask again, `Enter` pull; `Esc` steps out of a
  line, then clears a filter, then closes.
- **L13 — `--no-registry` refuses to open; read-only browses.**
- **L14 — One registry client for Upstream and the browser.**
- **L15 — The sandbox serves a real loopback registry.** `fakeregistry.Listen`,
  `fake-dockerd -registry 127.0.0.1:0`; a fake digest pull records the digest as the containerd
  store does.
- **L16 — Left out.** See Non-goals.

## Risks / Trade-offs

- Registries that ignore `n` send one large page; the cap still holds.
- Registries that page without `Link` (none known among Docker Hub, GHCR, ECR, Harbor,
  distribution) would show one page only.
- Pulling by digest leaves no local tag; an operator who wants `name:tag` locally must still
  pull the tag (or tag it with the CLI). Said in the dialog.
