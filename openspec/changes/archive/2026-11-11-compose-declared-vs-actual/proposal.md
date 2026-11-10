# Proposal

## Why

A compose project today is a label group: the desk shows its containers, its working
directory and its config-file path, and says it never reads the file. That turns a claim
into a fact no one checks — an operator can edit `compose.yaml` for weeks and still believe
the running stack is what the file declares.

The desk already earns its place by comparing one side against another and refusing to
guess where it cannot (Changes, Upstream, Packages). Comparing a project's file against
what runs is the same shape: a claim that can be checked. The comparison is pure code; the
liability is the parser, and the product is the list of rows it will *not* compare.

## What Changes

- A Compose project's Details gains a **Declared** tab, shaped like Changes: it shows only
  cheap facts (project, container count, working directory, config paths) and states that
  nothing is read until `Enter`. `Enter` runs one bounded read as a job and compares:
  - declared services against containers by `com.docker.compose.service`;
  - each service's declared `image` against the container's image, when the declared value
    is a literal and normalises to a comparable reference;
  - the file's write time against the newest container's creation, reported as a fact;
  - the `com.docker.compose.config-hash` already recorded on the containers, as evidence.
- **BREAKING (product promise):** the desk may now read *one* compose file, on request,
  and must say so. The requirement that it "SHALL NOT read, locate, parse or execute
  compose files" is relaxed to "may read, never execute": no `up`, `down`, `build`, `pull`,
  `scale`, no compose binary, no interpolation. `docs/DECISIONS.md` D15's text and the
  README's "Compose, honestly" and "What it does not do" sections move with it, as does
  the sentence in `internal/ui/details.go` that promises files are never read.
- Reading is gated: local `unix://` transport only, the path must be a regular file, at most
  1 MiB, the file's bytes are never rendered, and `--no-compose-files` forbids the whole
  class the way `--no-registry` forbids registry requests.
- Nothing compared that cannot be compared: a value containing `${` is reported as a
  template and not compared (the desk does not interpolate); a service that declares
  `profiles` is not called missing; `oneoff` containers are excluded; `deploy.replicas`,
  ports, volumes, environment, command and healthcheck are out of scope.
- The sandbox seeds real compose files and points the existing labels at them, including
  one project whose file is deliberately absent, so the "could not be compared" path is
  demonstrated rather than assumed.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `compose-projects`: the requirement "Projects are derived from container labels only"
  no longer forbids reading the config file the labels name; a new requirement defines the
  on-request comparison, its refusals and the guarantee that the file is never executed.

## Impact

Code: a new `internal/composefile` (parse + compare, plain structs, no daemon or UI
imports), `internal/plan` (comparison result), `internal/ui` (Declared tab, job wiring,
`--no-compose-files`), `internal/dockerctx` (transport gate already available),
`internal/fakeengine` + `make sandbox` (fixture files), `cmd/docker-desk` (flag).

Dependencies: one module, `go.yaml.in/yaml/v3` — the first runtime dependency added for a
file format, with the reasoning recorded against D15.

Docs: `README.md` (the Compose section and "What it does not do"), `docs/DECISIONS.md`
(new entry superseding D15), the `compose-projects` spec.

No new mutation paths, no new daemon endpoints, no writes to disk.
