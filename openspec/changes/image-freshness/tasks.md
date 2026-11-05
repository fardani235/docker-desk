# Tasks

## 1. Secrets and the Docker configuration

- [x] 1.1 Move `Secret` into `internal/secret` and make `forensics.Secret` an alias; verify the
  whole existing suite passes unchanged and no test's captured output gained a value.
- [x] 1.2 Export the configuration-directory rule (`$DOCKER_CONFIG`, then `~/.docker`) from
  `dockerctx`; verify both cases with a table test, including neither being set.
- [x] 1.3 Read `auths`, `credsStore`, `credHelpers`, `identitytoken` and `registrytoken`; resolve
  by the CLI's server key; verify the three-row key table, the Docker Hub legacy key, an empty
  username and a password containing a colon, from fixtures.
- [x] 1.4 Run a configured helper as a bounded subprocess with a validated name, a context, a
  timeout and `WaitDelay`; verify with a fake helper on `PATH` that asserts the server address it
  receives on stdin and that its output appears in no error, job line or log on failure.

## 2. The registry client

- [x] 2.1 Normalise references into host, repository and tag with `distribution/reference`;
  verify the table for bare names, `docker.io/…`, a host with a port, and a digest-only
  reference, which yields *not applicable* rather than an error.
- [x] 2.2 Send all four manifest media types, follow index → platform manifest → config blob,
  and skip a request whose digest already matches the local one; verify with an `httptest`
  registry that a current image costs one manifest request and a moved one costs the full walk.
- [x] 2.3 Answer a Bearer challenge, cache the token per host, realm, service, scope and
  credential, retry once on `401`, and fall back to Basic; verify with a fake registry that
  challenges, that refuses a stale token once, and that serves its realm from another host.
- [x] 2.4 Return typed errors for a missing manifest, a refused credential, a refused token
  request, a quota refusal with its reset time, and a name, TLS or timeout failure; verify each
  with a test that asserts the type and not the message text.
- [x] 2.5 Cap response bodies, refuse a redirect that downgrades to plain HTTP, strip
  `Authorization` when a redirect changes host, allow plain HTTP for loopback only, and verify the
  config digest against the manifest's before reading it; verify each with a test, including an
  oversized body and a cross-host redirect that must not receive the header.

## 3. The verdict and the caches

- [x] 3.1 Build the five verdicts and their sentences; verify a test per state, including an
  absent `created`, the epoch, an earlier upstream build and an identical one.
- [x] 3.2 Compare against `RepoDigests` and never the ID alone; verify the table for a containerd
  store (ID equals the index digest) and a classic store (ID is the config digest).
- [x] 3.3 Report the shared layer prefix, the count that differs, and a local platform absent
  from the index; verify with the explored 13-layer pair and with a platform that is missing.
- [x] 3.4 Keep the descriptor by host, repository and tag, the config by digest, the local facts
  by image ID, and the fallback answer by daemon; verify that a second open sends no request and
  that an age is kept with each entry.
- [x] 3.5 Add a registry counterpart to `testguard`; verify that a test which tries to reach a
  real registry fails loudly and that the ordinary suite never opens an outbound connection.

## 4. Engine, fallback and the fake engine

- [x] 4.1 Add `DistributionInspect` to the engine interface, through the existing transport and
  the read-only wrapper, and add it to the read-only coverage list; verify with the coverage test
  and a read-only run.
- [x] 4.2 Decide the fallback from the typed error alone and report `via the daemon` without a
  build time; verify that transport and quota failures fall back, that `current`, `moved`, `gone`,
  `401` and `403` do not, and that both failures produce one line with both reasons.
- [x] 4.3 Encode the fallback credential with `authconfig.Encode` and send it as
  `X-Registry-Auth`; verify the header against the encoder's own output byte for byte.
- [x] 4.4 Distinguish an endpoint the daemon does not serve from a manifest the registry does not
  have; verify both with a fake daemon at an old and a new API version.
- [x] 4.5 Serve `/distribution/{name}/json` from the fake engine and seed a moved, a current and
  a gone tag; verify `make sandbox` shows the tab, the fallback and the list column with no
  network at all.

## 5. The surfaces

- [x] 5.1 Add Images › Upstream after Findings, naming the host and whether a credential is
  involved before it asks, running the check as a cancellable job, with `r` asking again and the
  age shown; verify at 80×24 and 120 columns in every theme and in read-only mode.
- [x] 5.2 Add the `UPSTREAM` column at the lowest priority holding one word, with `+4d` only when
  a time was obtained; verify it drops first at 80 columns and that an unchecked image is blank.
- [x] 5.3 Run `U` as one sequential, cancellable job that skips ineligible images, stops at a
  quota refusal and reports the reset; verify with a fake registry that counts requests and
  refuses the third, and with a cancel midway.
- [x] 5.4 Add `--no-registry`, refuse every registry request under it, and say so on the tab;
  verify with a test that fails if any request is attempted, and with `--read-only` proving the
  check still completes.
- [x] 5.5 Document the tab, the column, `U` and the flag in `README.md`, `TESTING.md`, `help` and
  `docs/DECISIONS.md` round eleven; verify every documented command runs as written.

## 6. Integration

- [x] 6.1 Log in to the fake registry with `docker login` into a temporary `DOCKER_CONFIG`, then
  resolve the same credential from the desk; verify the server key, the base64 shape and the
  helper path agree with the CLI.
- [x] 6.2 Sweep every surface for a credential: job lines, errors, the tab, the inspect document
  and the list; verify with a test that searches the captured output for the fixture's secret.
- [x] 6.3 Run `make test`, `make race` and the e2e suite; verify all pass and that no test
  contacted a real registry.
