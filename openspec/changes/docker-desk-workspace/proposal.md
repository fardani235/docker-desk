## Why

Working with Docker from a terminal means answering one question ("what's running, why is it
behaving that way, and let me act on it") with five disconnected commands — `ps`, `logs -f`,
`stats`, `inspect`, `exec` — none of which stay on screen long enough to form a picture. Docker
Desk replaces that dance with one full-screen, keyboard-driven workspace in which the object
list, its details, the engine's own event stream and the progress of slow operations are all
visible and live at the same time, and in which every mutation is deliberate by construction
rather than by the operator being careful.

## What Changes

This is a greenfield repository (only a one-line README and the OpenSpec scaffolding exist), so
everything below is new.

- A single installable terminal program, `docker-desk`, that opens a full-screen multi-pane
  workspace ("the desk") against the one Docker daemon the current Docker context points at.
- Five always-visible panes: **Kinds** (containers, images, volumes, networks, compose
  projects), **List** (objects of the picked kind), **Details** (tabbed: info / logs / stats /
  raw inspect for the highlighted object), **Events** (the engine's live event stream) and
  **Jobs** (tracked background operations). The focused pane is unmistakable; everything is
  reachable from the keyboard.
- Live feedback loop: the engine event stream is shown as it happens and also drives list
  refresh, so the result of an action appears without re-running anything.
- Slow operations (image pull, prune, whole-project stop/remove, and in fact every mutation) run
  as tracked background jobs with progress, result and error retained; the desk never blocks on
  them.
- Container actions: start, stop, restart, pause, unpause, kill, remove; logs with live follow;
  live CPU / memory / network / block-I/O; an interactive shell inside a running container that
  takes over the terminal and hands it back cleanly.
- Image actions: pull by name, remove, prune unused.
- Volume and network actions: create, remove, prune; for networks, connect and disconnect
  containers.
- Compose projects: containers grouped by the `com.docker.compose.project` label Docker already
  carries; stop or remove the whole group. No compose files are read; no `up`, `down`, `build`.
- Safety model for a control plane:
  - the daemon endpoint is permanently on screen;
  - no single keystroke can mutate or delete — every mutation goes through a confirmation that
    names the exact object, the exact effect and the daemon;
  - deletions are never forced: in-use objects are reported as in use and refused;
  - a prune shows the exact objects it will destroy, then deletes exactly those and nothing else;
  - daemon errors are shown and the desk stays usable;
  - if the daemon is unreachable at launch the program says so plainly, names the endpoint and
    does not open the workspace.
- A `--read-only` launch mode in which every mutating request is refused before it reaches the
  daemon (used to smoke-test against a real daemon with a guarantee of no side effects).
- A fake Docker Engine (speaking the real Engine HTTP API on a unix socket) used by the test
  suite and runnable as a sandbox, so that every mutating path is proven without touching any
  real container.
- Documentation written alongside the build: README (install, launch, keys), a decision log with
  the reasoning and the rejected alternatives, and a build journal.

### Explicitly out of scope

- More than one daemon: no context switching at runtime, no picking among daemons.
- Daemons reached over the network (`tcp://`, `ssh://`): refused at launch with an explanation.
- Swarm (services, nodes, stacks, secrets, configs).
- Compose files and compose lifecycle (`up`, `down`, `build`, `pull`, scaling, profiles).
- Image builds, image push, registry login, image tagging.
- Kubernetes.
- Creating/running new containers, editing container configuration, copying files in or out.
- Forced removal of in-use objects.
- Disk-usage accounting (`docker system df`), e.g. per-volume sizes.

## Capabilities

### New Capabilities

- `daemon-connection`: which daemon the desk talks to, how that is resolved and displayed, launch
  refusal when unreachable or remote, behaviour on daemon errors and connection loss, read-only
  mode.
- `workspace-layout`: the panes, focus indication, keyboard navigation, resizing, help, quitting.
- `resource-browsing`: the five kinds, their object lists, filtering, and the details views of
  the highlighted object.
- `engine-events`: the live engine event stream pane and event-driven refresh.
- `background-jobs`: tracked, non-blocking jobs with progress, outcome, output and cancellation.
- `mutation-safety`: confirmation rules, in-use refusal, exact-set prune, error reporting for
  anything that changes or deletes.
- `container-operations`: lifecycle actions, logs and follow, live stats, interactive shell.
- `image-operations`: pull, remove, prune.
- `volume-network-operations`: create, remove, prune for volumes and networks; connect and
  disconnect for networks.
- `compose-projects`: label-based grouping and whole-project stop/remove.
- `live-stream-responsiveness`: bounds that keep the desk responsive under several noisy live
  streams.

### Modified Capabilities

None — there are no existing specs.

## Impact

- **New code**: a Go module (`cmd/docker-desk`, `cmd/fake-dockerd`, `internal/...`), a
  `Makefile`, tests, and documentation under `docs/`.
- **Dependencies**: Go ≥ 1.24 toolchain to build; the official Moby Engine API client; the
  Bubble Tea / Lip Gloss terminal UI libraries; `golang.org/x/term` and a pty library (the latter
  for tests only). No runtime dependency on the `docker` CLI binary.
- **Systems**: talks to the local Docker Engine API over its unix socket with the invoking
  user's permissions. It issues mutating API calls only after an explicit in-app confirmation.
- **The developer's machine during this change**: tests run exclusively against the fake engine
  on a temporary socket; the real daemon is only ever contacted in read-only mode.
