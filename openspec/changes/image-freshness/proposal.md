# Proposal

## Why

The desk can say that a tag moved on this daemon — `supply.tag-moved` compares a container's
image with the image the tag names here — but it never asks the registry. An operator cannot
tell whether pulling the tag again would bring a different build, or whether the build the tag
names now was made after theirs. The daemon keeps the digest it pulled; it does not keep the
build time of what the tag names today.

## What Changes

- A new **Images › Upstream** tab, after Findings. Opening it asks the registry for the
  highlighted tag as a job, and shows both identities, both build times, the shared layer
  prefix, and a verdict: current, moved, gone, unknown, or not applicable. It never calls a
  build newer or stale; it reports the times and the layer difference.
- A desk-side registry client: reference normalisation; anonymous and credentialed Bearer or
  Basic; index → platform manifest → config blob; token cache; rate-limit headers; redirect
  handling; bounded bodies. Nothing is written to disk.
- Credentials from the operator's Docker configuration (`auths`, `credsStore`, `credHelpers`),
  looked up by the Docker CLI's server key, scoped to the exact host, and never printed or
  logged.
- A fallback: when the desk cannot reach the registry (DNS, TLS, timeout, 429, an unreadable
  answer), it asks the daemon's `GET /distribution/{name}/json` with the same credential. That
  answer can say moved or current, but not when the upstream build was made; the tab says so.
- An **Upstream** column in the Images list, at the lowest priority, filled only after a
  check; `U` checks every eligible image as one sequential, cancellable job that stops at 429.
- `--no-registry` forbids all registry egress. Read-only mode is unchanged: every request the
  check makes is a GET or a HEAD.

## Capabilities

### New Capabilities

- `image-freshness`: the check, the comparison, the credentials, the fallback, the verdicts,
  the surfaces, and the refusals.

### Modified Capabilities

- None. The image list already promises "at least" the CLI's columns, and the tab is additive,
  as Changes was. No existing behaviour changes.

## Impact

- `internal/registry`: new — reference normalisation, credential resolution, token cache,
  registry protocol, typed errors, secret handling.
- `internal/freshness`: new — verdicts, caches, the fallback decision.
- `internal/engine`: `DistributionInspect` (read-only) and its place in the read-only coverage.
- `internal/fakeengine`: `/distribution/{name}/json` and seeded moved tags.
- `internal/ui`: `upstream.go`, the tab, the list column, `U`, hints and help.
- `cmd/docker-desk`: `--no-registry`.
- No new dependency: `distribution/reference`, `opencontainers/image-spec` and the moby API
  module are already direct.
- No mutation, no write to disk; read-only mode unchanged.
