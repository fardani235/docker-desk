# Proposal

## Why

The Changes tab already answers the operator's question — the image a container runs is older
than the image its tag names now — and then stops at one sentence: restarting keeps the old
image, and only re-creating takes the new one, which the desk does not do. Applying an update
therefore means leaving the desk, reading `docker inspect` by hand, and rebuilding the command.
That is exactly where configured details are lost: environment, mounts, networks, restart
policy, health check, resource limits.

The desk can now do this itself. The create round (seventeen) proved a plan can pull, create
and start as one job; the Changes round (eight) proved the drift can be identified exactly; the
mutation-safety model is mature enough for a stop that is not a stop-and-lose-everything. What
is missing is the verb between the finding and the operator's shell.

## What Changes

- **A re-create action on Containers › Changes.** When the comparison finds that the tag names
  a newer build of the same platform, the tab offers `A`. It sends nothing on open; the reads
  happen only when the key is pressed. Read-only refuses it. It applies to one container, and
  refuses while the kind has a selection.
- **The plan is exactly four steps:** stop the old container (if it is running), rename it to a
  derived name recorded in the confirmation, create the new container under the original name
  from the new build, and start it. One job, in that order, each step skipped when its
  predecessor failed.
- **The configuration is cloned from the container's own inspect, or the action refuses.**
  Image, name, command and entrypoint, environment, working directory, user, hostname, TTY,
  labels, health check, stop signal and timeout, ports and their bindings, mounts, restart
  policy and retry limit, network attachments with their aliases, and the common resource
  limits are carried. Settings outside that set — privilege, capabilities, devices, security
  options, namespaces, a custom runtime, sysctls, tmpfs, DNS and host overrides, links, log
  driver configuration, a read-only root filesystem, auto-remove, and borrowed network
  namespaces — cause a refusal that names every one of them.
- **Environment values are carried and never displayed.** The confirmation, its effects, the
  job, the job output and the status line name variables and counts only. The values exist in
  the plan because the daemon needs them.
- **The tag is pinned to the build the dialog showed.** Immediately before the first step the
  plan re-checks that the reference still names that image. If it has moved, nothing is sent —
  not even the stop.
- **The old container is never deleted.** It is renamed and left stopped, and both the
  confirmation and the job say so. A failure after the rename leaves it stopped under that name
  and names it, so the operator can rename it back or remove it deliberately.
- **The confirmation is typed:** the operator types `recreate`, as removals are typed.
- **Nothing is pulled.** The comparison works only when the new build is already on the daemon;
  a reference that no longer resolves here offers nothing.

### Non-goals

- Cloning settings outside the reproducible set; the refusal is the answer.
- A pull, a retag, an automatic rollback, or removing the old container.
- A second entry point in the action menu, a key elsewhere, or a CLI flag.
- Reading, interpolating or running a compose file; compose labels are carried as labels, and
  the confirmation states the consequence of the old container staying.
- Copying a container to another daemon, or re-creating from an image ID or digest reference.

## Capabilities

### New Capabilities

- `container-recreation`: when a re-create is offered, what the plan does, which configuration
  is carried and which is refused, how the environment and the pinned build are handled, what
  the confirmation and the job say, and what happens on failure at each step.

### Modified Capabilities

- `image-changes`: the closing note changes from "which this desk does not do" to the offered
  action and its key, and to the reason when no action is offered.
- `mutation-safety`: re-creation is a new confirmation category — typed with its own word —
  because it stops a running container and changes its identity before creating another.

## Impact

- `internal/engine`: `InspectContainer` must expose the settings the clone needs (environment
  values, entrypoint, bindings, restart limit, network aliases, health check, stop behaviour,
  resources); `CreateContainer` options gain the clone fields; a new `RenameContainer`; the
  read-only wrapper and its coverage count; real and fake request shape.
- `internal/plan`: a new operation and plan builder, the clone derivation and its refusal list,
  the previous-name rule, a plan-level pin checked before any step, and the job's reading.
- `internal/ui`: `A` on the Changes tab, the confirmation rendering, the tab's note and footer,
  help text, read-only and selection handling, and the refresh after the job.
- `internal/fakeengine`: a rename endpoint, create fidelity for the new fields, and an inspect
  answer rich enough that a re-created container can be re-created again.
- Tests and evidence: unit tests for derivation and refusals, plan and execute tests, UI and
  end-to-end tests, a `docker create`/`docker rename` cross-check, and a read-only real-daemon
  derivation check; README, TESTING and DECISIONS entries and captured screens.
