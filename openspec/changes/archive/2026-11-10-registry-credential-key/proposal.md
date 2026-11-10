# Proposal

## Why

The desk chooses a registry credential by matching host names across every entry in `auths`,
so several entries for one host — Docker Hub's real key plus the `access-token` and
`refresh-token` entries Docker Desktop writes beside it — all match, and the winner is map
iteration order. In one measured run of 200 resolutions the wrong entry was chosen 33% of the
time, and the token service answered 401 `incorrect username or password`. The operator sees an
intermittent "the token service refused the request", while `docker` with the same
configuration keeps working, because the CLI looks the exact server key up first. A refused
token is definitive and is never retried through the daemon, so the check fails instead of
degrading.

## What Changes

- Credential resolution selects a registry's entry by the Docker CLI's **exact server key**
  first, and only then falls back to the host-name comparison that serves genuinely legacy
  configurations. `https://index.docker.io/v1/` therefore wins over sibling keys that share its
  host, deterministically and on every call.
- The same exact-first rule applies to `credHelpers`, whose lookup has the identical hazard.
- A configuration whose only entry for a host is a sibling key still resolves, because the
  host fallback remains for that case.
- Tests pin the choice: a fixture with the three Docker Hub keys must always resolve to the
  legacy key, and the fixture must fail if the map iteration order is the decider.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `image-freshness`: the requirement "Credentials come from the Docker configuration" now says
  the credential is chosen by the server key the Docker CLI files it under, before any host-name
  comparison, so a sibling entry for the same host cannot be chosen.

## Impact

- `internal/registry/creds.go`: `ResolveCredential` and `lookup` gain an exact-key pass; the
  host fallback stays behind it.
- `internal/registry/creds_test.go`: a three-key Docker Hub fixture, and the legacy-only case
  that must still resolve.
- No change to the wire protocol, the token cache, the verdicts, the daemon fallback or read-only
  mode. Repository browsing, Image digests and Images › Upstream all inherit the fix because
  they share `ResolveCredential`.
- No new dependency, nothing written to disk, no secret printed.
