# Tasks

## 1. Exact-key preference

- [ ] 1.1 Give the `auths` branch of `ResolveCredential` an exact-key pass before its host-name
  fallback (`internal/registry/creds.go`); verify with a fixture holding the Docker Hub legacy
  key plus `…/access-token` and `…/refresh-token`, resolving 200 times and asserting that every
  resolution returns the legacy key's credential, which the previous order fails intermittently.
- [ ] 1.2 Apply the same exact-first order to the `credHelpers` lookup; verify with a fixture
  whose `credHelpers` holds an exact key and a sibling key for the same host, and a fake helper
  that records which name it was invoked as, asserting the exact key's helper runs every time.
- [ ] 1.3 Keep the host-name fallback behind the exact pass; verify a fixture whose only entry
  for a host is a legacy key still resolves, and one with no entry for the host still resolves to
  no credential rather than an error.

## 2. Regression across the shared path

- [ ] 2.1 Run `go test ./internal/registry ./internal/freshness ./internal/repository ./internal/ui`
  and confirm the repository browser and Upstream paths still resolve credentials from the same
  configuration.
- [ ] 2.2 Confirm the new fixtures keep every credential value out of test output, error text and
  `Credential.Source`, by asserting the source is only `auths` or a helper name.

## Workflow follow-up

- Archive the change once review has passed and `openspec validate registry-credential-key --strict` is clean.
