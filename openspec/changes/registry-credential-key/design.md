# Design

## Context

`internal/registry/creds.go` chooses an entry from two configuration maps, `auths` and
`credHelpers`. Before this change `credHelpers` (through `lookup`) already tried `m[key]` first,
but `auths` went straight to the host-name loop, and so did the `auths` check in
`CredentialSource`. `ResolveCredential` and `CredentialSource` are shared by freshness checks,
the repository browser and Images › Upstream (see proposal.md, Impact). Recorded while the
change was being applied, because no design artifact existed beforehand.

## Goals / Non-Goals

**Goals:**
- One selection rule for every configuration map, used by both `ResolveCredential` and
  `CredentialSource`, so what the tab says before it asks matches what is then sent.
- The same selection on every call, including within the legacy fallback.

**Non-Goals:**
- Changing the order between sources (credHelpers → credsStore → auths).
- Narrowing `sameServer` to the CLI's stricter `serverAddress == ConvertToHostname(r)` check.
  That would drop legacy matches the desk accepts today, such as `https://ghcr.io/` for
  `ghcr.io`.

## Decisions

- **A single generic selector, `serverEntry[V any](map[string]V, key)`.** It tries the exact key
  first and falls back to the host name. `lookup` (credHelpers), the `auths` branch of
  `ResolveCredential` and `CredentialSource` all call it. *Alternative:* add an exact pass in
  each of the three places separately. Rejected because the three could drift apart, and
  `CredentialSource` is precisely the place that drifted.
- **The fallback walks keys in sorted order.** The spec only requires the exact key to win.
  Without this, several legacy keys for a host that has no exact entry would still be chosen
  by map iteration order. This costs nothing. *Alternative:* leave the fallback unordered.
  Rejected because it keeps the original hazard in the legacy case.
- **An exact entry wins even when it is empty or malformed.** This matches the CLI's file store,
  which returns `auths[serverAddress]` when that key is present. A malformed exact entry is
  an error, not a reason to fall back to a sibling. *Alternative:* skip empty or invalid
  exact entries and fall back. Rejected because it would reintroduce sibling selection for
  Docker Hub. (An empty `{}` under the Hub key, as Docker Desktop writes it, comes with a
  `credsStore`, and that store is consulted before `auths`.)
- **Tests include a sibling that sorts ahead of the server key**
  (`http://index.docker.io/v1/access-token`). Because of the sorted fallback, the three-key
  Docker Hub fixture would also pass without the exact pass (a prefix sorts first). The
  extra sibling makes the exact pass the only thing that can make the test pass. Verified by
  running the tests against the pre-change code (a sibling chosen 173/200 times) and against
  a mutant without the exact pass (fails in both `auths` and `credHelpers`).

## Risks / Trade-offs

- [A configuration whose exact key holds an empty `auths` entry while a sibling holds a real
  credential, with no credsStore, now resolves to no credential] → This is what `docker`
  itself does with the same file, and the request goes out anonymous rather than with a
  credential filed for a different purpose.
- [The fallback's choice among several legacy keys is now lexical, not meaningful] → It applies
  only when no exact key exists. It is deterministic, so a wrong choice is reproducible and
  can be diagnosed.
