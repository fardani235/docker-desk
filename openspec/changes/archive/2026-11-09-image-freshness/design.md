## Context

The desk talks to one daemon for its whole lifetime (`daemon-connection`) and never to a
registry; `supply.tag-moved` compares two local images and says nothing about upstream. The
proposal's Why explains the gap. Three facts about the current code shape this design: the
engine interface is deliberately small and every read is covered by the read-only list
(`mutation-safety`); image facts are already kept by image ID and archived facts are already
kept by image and platform (`image-changes`, `image-findings`); and the project already holds
two direct dependencies this needs — `distribution/reference` and the moby API module, whose
`authconfig.Encode` is the daemon's own encoding of a registry credential.

Two facts about the live registries were established before writing this. Docker Hub and GHCR
both answer an anonymous manifest request with `401` and a Bearer challenge that names its
realm, so the first request of a check is cheap and usually enough for a public image. Docker
Hub's anonymous quota is per source address and shared with `docker pull`: the header read
during exploration was `100;w=3600`, 96 remaining. The checks are not free, and they compete
with the pulls that a containerisation workflow depends on.

## Goals / Non-Goals

Goals: one question answered honestly — does the tag this image carries name a different build
today, and if so what differs — from the operator's own credentials, in the operator's own
network, without a new dependency and without a second meaning of "now".

Non-goals: ordering versions, listing a repository's tags, judging whether either build is
good, pulling, re-creating, a background poll, a private certificate authority, native ECR or
GCR signing, mirrors on the desk side.

## Decisions

Recorded in full in `docs/DECISIONS.md`, round eleven (F1–F12). In short:

### F1 — The desk becomes a registry client; the verdict is its own capability

**Decided:** a new capability `image-freshness` with two new packages, `internal/registry` (the
wire) and `internal/freshness` (the comparison, the caches, the fallback decision), and one UI
file. Nothing in `image-operations` or `resource-browsing` changes: the image list promises at
least the CLI's columns, and a sixth tab is additive, as Changes was. **Alternative:** a delta
on `image-operations` — it would describe a registry in a capability that never had one.

### F2 — Four requests, and the third only when it can differ

**Decided:**

```
GET  {realm}?service=…&scope=repository:{repo}:pull      (only on a Bearer challenge)
GET  /v2/{repo}/manifests/{tag}                           index or single manifest
GET  /v2/{repo}/manifests/{child}                         only when the tag's digest differs
GET  /v2/{repo}/blobs/{config}                            created, rootfs.diff_ids, history
```

The manifest request sends all four media types the desk understands, so an index, a manifest
list, an OCI manifest and a schema-2 manifest all arrive without a second negotiation. The
child request is skipped when the tag's digest is already the local one, and the config request
is skipped when the child's config digest is already the local one. **Alternative:** always
fetch the config — two requests that cannot change the verdict.

### F3 — The local digest is `RepoDigests`, never the image ID

**Decided:** the local side of the comparison is the image's `RepoDigests` entry for the
repository, normalised with `distribution/reference`. The image ID is compared only as a last
resort and never alone: on this machine's containerd store ID, `RepoDigests[0]` and the tag's
index digest coincide, and on a classic store the ID is the config digest and can never equal
an index digest. A comparison written against the ID passes here and fails silently on a
DAEMON with the older store. **Alternative:** the ID — simpler, and wrong on half the fleet.

### F4 — The repository is normalised before anything is asked

**Decided:** `distribution/reference` produces host, repository and tag; a reference with no
host is Docker Hub, whose canonical host for requests is `registry-1.docker.io` and whose
canonical name is `docker.io/library/alpine`. The request path uses the familiar name
(`library/alpine`), because that is what the registry accepts. A reference with a digest and no
tag is *not applicable*, not an error.

### F5 — Credentials: the CLI's key, then helpers, then `auths`

**Decided:** resolution order per host: `credHelpers[key]`, then `credsStore`, then `auths[key]`,
then anonymous. The key is the CLI's server address, not the request host:

| reference | host asked | configuration key |
| --- | --- | --- |
| `alpine`, `docker.io/library/alpine` | `registry-1.docker.io` | `https://index.docker.io/v1/` |
| `ghcr.io/acme/api` | `ghcr.io` | `ghcr.io` |
| `registry.internal:5000/api` | `registry.internal:5000` | `registry.internal:5000` |

The configuration directory is `$DOCKER_CONFIG`, or `~/.docker` (`dockerctx.go:118`); that rule
is exported and shared rather than copied. `auths` is standard base64 split at the first colon.
`identitytoken` and `registrytoken` are bearer tokens and are used as such, not ignored. A
helper is a subprocess: its name is validated against `[A-Za-z0-9][A-Za-z0-9._-]*` before
`exec.LookPath`, it gets a context, a timeout and `WaitDelay` — because `CommandContext` kills
only the direct child — and its output is bounded and never echoed. The configuration is
re-read per check, so a `docker login` elsewhere takes effect without restarting.

### F6 — A secret that prints as a mask, in its own package

**Decided:** the `Secret` value moves from `internal/forensics` to a new `internal/secret`, and
`forensics.Secret` becomes an alias so no call site changes. Registry tokens and helper output
use the same type. The plain string exists only for the length of one request; nothing writes a
credential to disk, a job line, an error, a view or the inspect document. **Alternative:** a
second `Secret` in `internal/registry` — two implementations of one invariant.

### F7 — Anonymous first, then the challenge; tokens cached per scope

**Decided:** the first manifest request is anonymous. On `WWW-Authenticate: Bearer` the desk
fetches a token from the named realm with the named `service` and `scope` and retries once; on
`Basic` it sends Basic. The token cache is keyed by registry host, realm, service, scope and the
identity of the credential, lives in memory only, and is dropped on a `401` with one retry — a
second `401` is final. A realm on another host is normal; the credential sent to it is the one
configured for the *registry* host, so a hostile registry cannot cause a credential for a
different registry to be sent. **Alternative:** Basic only — Docker Hub and GHCR both refuse it
for pulls.

### F8 — The fallback is sequential, bounded, and only for an absent answer

**Decided:** the desk-side attempt has a bounded timeout of about ten seconds; only then does
the daemon get asked, through the engine's `DistributionInspect`, with the same credential
encoded by `authconfig.Encode` as `X-Registry-Auth`. It falls back on a name, connection or TLS
failure, a timeout, a quota refusal, an unreadable answer, or a helper that failed locally. It
does not fall back on `current`, `moved`, `gone`, `401` or `403`: those are answers, and the
daemon would send the same credential. The fallback's answer carries `via the daemon` and never
a build time or a layer comparison. A `404` on the endpoint is *cannot ask*, distinct from a
`404` naming a missing manifest, which is *gone*. **Alternative:** ask both in parallel — two
requests where one normally answers, and a race whose result depends on which finished first.

### F9 — Every state names its cause, and none says stale

**Decided:** `current`, `moved`, `gone`, `unknown`, `not applicable`. `moved` carries the two
build times when known, which is later and by how much, and *first N of M layers shared*. The
times are producer-controlled text, not truth: a reproducible build can carry the epoch and a
rebuild can be older than what it replaced, so a direction is reported only when both times
parse and differ, and the source of the time is named. Quota, refusal, unreachable, unverified
and not-published-for-this-platform are separate sentences. `stale` and `newer` appear nowhere.

### F10 — Caches keyed by what cannot change meaning

**Decided:** the descriptor by registry host, repository and tag; the config by digest; the
local facts by image ID, reusing `ImageFacts`; the fallback answer by daemon endpoint,
repository and tag, because two daemons can see different mirrors. Each entry holds the time it
was obtained; the tab shows that age; `r` replaces it; a batch shares the same caches. No
background job polls, and a plain list refresh asks nothing. **Alternative:** a TTL — a hidden
second meaning of "current", and a request the operator did not ask for.

### F11 — The surfaces: one tab, one column, one batch key

**Decided:** Images › Upstream after Findings, opened by `]` as every tab is. The list gains
`UPSTREAM` at the lowest priority, so it drops first at 80 columns, and it holds one word
(`moved`, `current`, `—`); the `+4d` is only shown when a build time was obtained. `U` runs the
batch as one cancellable job; `c` cancels. The tab names the host before it asks. The column is
filled only by a check and by nothing else, so an unchecked image is honestly blank rather than
guessed. **Alternative:** colour the repository cell — the list's colour already means one
thing.

### F12 — Egress is a switch, and trust is not widened

**Decided:** `--read-only` does not forbid the check, because every request is a GET or HEAD;
`--no-registry` forbids all registry egress and is the air-gap switch. TLS uses the system
roots; plain HTTP is allowed for loopback hosts only, and there is no private-CA flag in this
version — the daemon fallback still answers `moved` or `current` for such a registry, without a
time. Bodies are capped (manifests at a few megabytes, configs at thirty-two), the `Location`
header may not downgrade to plain HTTP, and the `Authorization` header is dropped whenever a
redirect changes host. Response text is untrusted and goes through the display sanitiser.
**Alternative:** reuse `--read-only` as the switch — it means "do not change the daemon", and
this changes nothing.

## Risks / Trade-offs

- **A tag can move between the manifest request and the config request.** → The verdict names
  the digest it read and the time it read it; a batch caches the descriptor so its config is
  fetched for the digest it saw, not for the tag again.
- **A registry can answer with a different platform than the local image.** → The comparison
  reports *your platform is no longer published* and compares nothing, rather than comparing
  one platform's config with another's.
- **The credential reaches a daemon over plain TCP in the fallback.** → The same exposure as
  `docker pull` over that link, the top line already says `NO TLS`, and the tab says the answer
  came through the daemon.
- **A mirror on the daemon makes the fallback disagree with the desk.** → Both are reported as
  what they are, with their source; the desk prefers its own answer whenever it has one.
- **Quota is per address and shared with pulls.** → A batch stops at the first refusal and
  reports the reset; checks are asked for, never automatic.
- **A private CA registry never gets a build time.** → It is a stated limitation of v1, not a
  silent one; the tab says the desk could not verify the registry.
- **A registry could serve a config that is not the one its manifest names.** → The config
  digest is checked against the manifest's before it is read.

## Migration Plan

No migration: the capability is new and no existing behaviour changes. `docs/DECISIONS.md`
gains round eleven, `README.md` gains the tab and the flag, `TESTING.md` gains the fake
registry, and `help` gains the tab and `U`. The work can land in three stages without weakening
the end state — anonymous public reads, credentials, then the batch and the list column — and
the verdict logic is identical in all three.
