# Design

## Context

See `proposal.md` — Why. The constraints that shape this work:

- The Changes tab already resolves the reference and the pair cheaply (`internal/changes`
  `FindPair`, `SamePlatform`), and renders "which this desk does not do" at `changes.go:587`.
  Its facts are cached by image ID; the tab is the only place the verdict exists.
- The create round produced a plan chain with declared dependencies
  (`plan.ContainerCreate`, D23): pull if missing, create (`ProducesID`), start (`IDFrom`).
  `plan.Execute` skips dependents of a failed step and logs what was already done.
- `engine.ContainerCreateOptions` models only the seven form fields. The engine's
  `createConfig` builds `container.Config` and `container.HostConfig`; it sends no
  `NetworkingConfig` and has no entrypoint, labels, TTY, health, stop, limits or networks.
- `engine.InspectContainer` keeps the whole daemon document in `ContainerDetail.Raw`, but its
  typed fields deliberately omit environment values (`EnvCount` only). The Inspect tab already
  draws `Raw`, values included; the create form and its job are where values must not appear
  (D29).
- Docker gives every container an immutable name and refuses a duplicate. A new container
  therefore cannot take the old one's name while the old one holds it. The engine has no rename
  method; the fake has an in-process `Rename` but no HTTP route.
- `plan` is pure and works from `Snapshot` (the four lists). It has no detail for a container;
  the UI holds the focused container's inspect in `m.detail`.
- Every mutation is overridden in `readonly.go` and counted by
  `TestReadOnlyCoversEveryMutation` (20 today). The suite never writes to a real daemon.

## Goals / Non-Goals

**Goals**

- One action, one plan, one job: stop (when needed), rename the old, create the new under the
  original name from the pinned build, start it.
- Reproduce what the container is, and refuse — naming every setting — what it cannot.
- Keep environment values out of everything the operator can see.
- Fail safely at every step, never deleting anything, and state exactly what remains.
- Prove the wire shape against the fake and the real CLI, and the derivation against a real
  container read-only.

**Non-Goals**

- Editing the configuration in the dialog. The operator updates an image, not a container.
- Pulling, retagging, rolling back, or removing the old container.
- Reproducing settings outside the carried set, or guessing at a Docker field the desk does
  not model.
- A second entry point, a CLI flag, a compose lifecycle, or a cross-daemon copy.
- Proving a re-create against a real daemon by writing to it; the project's guard forbids it.

## Decisions

### 1 — The entry point is `A` on Containers › Changes only

`onChangesKey` gains `A`. The tab's note becomes verdict-dependent; the footer lists `A` when
it is offered, and the help text gains the key. No container action key and no action-menu
entry are added.

*Why:* the verdict, the two image identities and the cached facts live on this tab; a menu entry
would have to read both images and then open a dialog from a place where the operator cannot see
what was compared. The tab is where the finding is, so the action belongs next to it.

*Alternatives:* a container action key (`A` from the List, reading the pair first) — refused for
this round as a second path to maintain; a menu entry that only opens the tab — noise.

Read-only is refused at the key before any request, and a non-empty container selection is
refused with a status sentence. `A` is not part of the `kindActions` machinery, so it carries no
`setOp` and cannot run on a selection.

### 2 — The plan is stop, rename, create, start; the old container is kept

```
 old: web (running, image A)
   |
   +-- stop        (only when active)
   +-- rename      web -> web-previous-3f9c1a2b
   +-- create      web from image B (tag pinned to B's ID)
   +-- start       the ID the create returned
   |
 new: web (running, image B)     old: web-previous-3f9c1a2b (stopped)
```

The derived name is `<name>-previous-<first 12 hex of the old ID>`, the base shortened if the
result exceeds a 128-character bound, and `-2`, `-3`, … appended while the snapshot already has
that name (to a small bound, then the plan is blocked). It is validated with the same `validName`
rule as a form name.

*Why keep the old:* removing it destroys the writable layer and the only rollback, and the
desk's promise is that nothing is deleted without a typed deletion of its own. Rename is
reversible, the old container keeps its configuration and data, and the operator can start it
again or remove it deliberately.

*Alternatives:* remove the old before creating (destructive and unrecoverable on a create
failure); create the new under a different name (changes the identity, so compose and DNS refer
to the wrong container until a rename, and two renames add two failure points); create first and
swap names (the same, plus a window with no container under the original name). Rejected.

*Consequence, stated in the dialog:* compose-aware tools may list the old container under the
same service, because its compose labels are carried. The compose file is never read.

### 3 — The clone is derived from the inspect document, as a closed model

`plan/recreate.go` gains `Clone(detail engine.ContainerDetail) (CloneSpec, []string, error)`.
It parses `detail.Raw` into a narrow struct of the fields it carries and the families it
refuses. The returned `CloneSpec` is the desk's own type; it converts to
`engine.ContainerCreateOptions` for the create step.

The carried set:

| Area | Fields |
| --- | --- |
| Identity | image reference, name, labels (compose labels included) |
| Process | entrypoint, command, working directory, user, hostname, TTY, init |
| Environment | every `NAME=value`, values included |
| Health | health check (test, interval, timeout, start period, retries) |
| Stop | stop signal, stop timeout |
| Ports | published bindings (host address, host port, container port, protocol) and exposed-only ports |
| Storage | bind and volume mounts, read-only flag; the volume name for anonymous volumes |
| Network | network mode (default, host, none), every user-defined attachment with aliases |
| Lifecycle | restart policy and retry limit |
| Limits | memory, swap, reservation, NanoCPUs, CPU shares/quota/period, cpuset, pids limit, shared memory size |

The refused set, each named in the dialog:

| Family | Detected from |
| --- | --- |
| Privilege | `HostConfig.Privileged` |
| Capabilities | `CapAdd`, `CapDrop` |
| Devices | `Devices`, `DeviceRequests` |
| Security | `SecurityOpt` |
| Runtime | `HostConfig.Runtime` |
| Kernel settings | `Sysctls`, `Ulimits`, `OomKillDisable`, `MemorySwappiness` |
| Tmpfs | `Tmpfs` |
| Host overrides | `ExtraHosts`, `Dns`, `DnsSearch`, `DnsOptions`, `GroupAdd`, `Links` |
| Logging | `LogConfig.Type` other than json-file, or non-empty `LogConfig.Config` |
| Root filesystem | `ReadonlyRootfs`, `AutoRemove` |
| Namespaces | non-default `PidMode`, `IpcMode`, `UTSMode`, `UsernsMode`, `CgroupnsMode`; `CgroupParent` |
| Network identity | `NetworkMode` of `container:<id>`, an attachment's `IPAMConfig` |
| Storage | a mount whose type is neither bind nor volume |

*Why a closed model:* an unmodelled field would otherwise be silently dropped, which is exactly
the class of mistake the feature exists to prevent. Refusing is honest and about as much work as
carrying.

*Alternatives:* post the inspect document back as the create body — faithful by construction,
but it carries runtime fields the create API rejects and bypasses the desk's narrow engine
types; model every Docker field — the desk becomes `docker run` and every new API version adds
work; a prefilled thirty-field form — does not answer the operator's request.

*Limitation, recorded:* the refusal list covers the HostConfig fields of the pinned Docker API,
not fields a future daemon might add. A field the desk does not know about is not detected. This
is stated in DECISIONS and TESTING rather than hidden.

### 4 — Environment values are read in the plan and rendered nowhere

The engine's typed detail keeps its `EnvCount` contract; the derivation reads the values from
`Raw` because the daemon needs them for the create. `CloneSpec` holds them, and the plan's
rendering, effects, warnings, job title, job target, job log, status line and error paths are
tested with a sentinel value.

*Why not extend `ContainerDetail`:* it would put values in every caller's hands. Keeping the
read inside the one function that needs them limits the surface. It is not a new exposure of the
daemon's data — the Inspect tab already draws the raw document — but it is a new place that must
not echo it.

*Alternatives:* refuse any container with environment (unusable); ask the operator to retype the
values (and lose them in the transcript); pass the values through the UI (same exposure, worse
layering).

### 5 — The engine grows one mutation and a wider create, and no new reads

- `RenameContainer(ctx, id, newName)` in `Engine`, refused by `readonly.go`, counted by the
  coverage test (20 → 21).
- `ContainerCreateOptions` gains `Entrypoint`, `Labels`, `Tty`, `WorkingDir`, `User`,
  `Hostname`, `Init`, `Healthcheck`, `StopSignal`, `StopTimeout`, `NetworkMode`,
  `Networks []NetworkAttachment`, `ExposedOnly []string`, `Resources ResourceLimits`,
  `ShmSize`.
- `createConfig` maps the new fields into `container.Config`, `container.HostConfig` and the
  request's `NetworkingConfig`. Zero values change nothing, so the existing form's request is
  byte-identical.
- `InspectContainer` is unchanged: `Raw` already carries everything the derivation needs.

*Why not a separate clone method:* it would duplicate the create path, hide the request from
the fake and the cross-check, and create two ways to create a container. One wider options
type is wider by exactly the fields the clone needs.

*Why `ExposedOnly` rather than a flag on `PortBinding`:* a port in the form means "publish"
  (an empty host port means the daemon picks one), while an inspect port with no binding means
  "expose only". Overloading one field with both meanings would change every existing caller;
  a separate list is explicit and additive.

*Why no engine read additions:* `Raw` is the daemon's answer; parsing it in the pure layer
keeps the engine's API narrow and the fake simpler.

### 6 — The derivation and the plan live in `internal/plan`

`plan/recreate.go` holds `Clone` (pure), `PreviousName` (pure), and
`ContainerRecreate(snap Snapshot, detail engine.ContainerDetail, spec CloneSpec, pin ImagePin)`.
It checks the original name against the snapshot excluding the container itself, checks that
each attached network exists, and builds the four steps with `DependsOn`, `ProducesID` and
`IDFrom`.

*Why here:* `plan` owns what a mutation will do and the frozen-snapshot rule; the clone is one
file of pure mapping. A new package would add a boundary with one consumer.

*Alternative:* `internal/recreate` — reconsider when a second consumer appears (for example a
`--recreate` evidence mode). Not now.

### 7 — The dialog re-reads the four facts it claims

Pressing `A` runs one `tea.Cmd` with a 10-second budget:

1. `plan.Take` (the four lists).
2. `engine.InspectContainer(id)` — the container's current configuration and reference.
3. `changes.FindPair(detail.Image, detail.ImageID, snap.Images)` — a pair or a refusal
   (pinned, from an ID, unresolved, same).
4. `engine.ImageFacts(oldID)` and `engine.ImageFacts(newID)` — identity, age, layer count and
   platform.
5. `changes.SamePlatform(old, new)` — the platform refusal.
6. `plan.Clone(detail)` — the spec or the refusal list.
7. `plan.ContainerRecreate(...)` with `Pin{Ref: pair.Ref, ID: pair.NewID}`.

The tab's cached facts gate whether `A` is offered; the dialog does not trust them for what it
claims. The cost is about nine GETs, no archive, on a key press.

*Why not reuse the tab's facts:* they may be minutes old, and the dialog's whole promise is
that it names the build the tag means now. A stale dialog would invite the operator to type the
word for a plan the pin then refuses.

*Alternative:* build from the cache and let the pin catch a move. Cheaper, but the operator
would confirm a plan against a build that is no longer the tag's.

### 8 — The pin is checked before the first step

`Plan` gains `PinRef string` and `PinID string`. `Execute` checks them before the loop, with
`InspectImage(PinRef)`, and returns an error when the ID differs (or the image is gone), so the
job fails without a single step. `plan.ContainerRecreate` also sets the create step's `Pin` for
local clarity, but the preflight is the guarantee.

*Why before the loop:* checking at the create step would stop and rename the old container
first, leaving it stopped and renamed because the tag moved. The point of the pin is that
nothing at all happens.

*Alternative:* a first "verify" step. Rejected: `Steps` are the mutations the dialog lists, and
a read in that list would make the dialog's step list untruthful.

### 9 — The confirmation is typed and states what remains

`Tier: TierTyped`, `Word: "recreate"`, `Target` the container, `Effects` the four steps with the
derived name, old and new image references and short IDs, and "Nothing is deleted; the old
container stays stopped under that name." `Warnings` state that bind sources are re-mounted
without a check, that any shell into the container ends, and the compose consequence when
compose labels are present. `Blockers` carry the refusal list (one line per family), so the
dialog opens and explains instead of a bare status line.

The table's other confirmations use `y` for reversible state changes and a typed word for
deletions. Re-creation is neither: it stops a running container and changes its identity, then
creates another. Typing a word is the category the operator already knows for the irreversible
and the identity-changing, and it binds the dialog to the object.

*Alternatives:* `y` with the summary (lighter, but the effect is larger than any `y` action:
stop plus rename plus create); typing the container's name (consistent with binding, but the
project's existing words are short, and compose names can be long).

### 10 — The tab offers `A` only when it can be true

The tab's offer needs: the moved verdict, both facts, the same platform, the loaded inspect, the
container still holding its own name, and `plan.Clone(detail)` returning no refusal. The clone
check is cached per container ID and inspect revision, not recomputed every frame. When a
condition fails, the note names the reason: the platform, the setting families, another
container holding the name, or a reference that cannot move.

After a successful re-create the old row is still selected under its derived name. Its tab still
compares its old image against the tag, but its name is now held by the new container, so the
tab says the name belongs to another container and offers nothing. This is how the tab avoids
offering a third container under a taken name.

### 11 — Failure is reported, never repaired automatically

`Execute` already skips dependents and its cancellation path logs what was done. The re-create's
effects must therefore be reportable step by step:

| Failure | What remains | What the job names |
| --- | --- | --- |
| stop | the container as it was | the daemon's refusal |
| rename | stopped, original name | the original name, nothing created |
| create | stopped, derived name | the derived name, nothing started |
| start | new container created, old as above | both names and the created state |

No automatic rollback. A rollback is a second mutation on a failing daemon; the operator is told
the exact name and can start or rename the old, or remove it, deliberately. Automatic removal is
never offered.

### 12 — The fake and the cross-check carry the wire shape

- `POST /containers/{id}/rename` in the fake: 204, updates the name, emits the event, 409 for a
  name in use, 400 for an invalid name.
- `handleContainerCreate` records the new fields (entrypoint, labels, TTY, working directory,
  user, hostname, init, health, stop signal and timeout, limits, shared memory, exposed-only
  ports, network attachments) and `handleContainerInspect` reports them, so a re-created
  container can be re-created again and the derivation is exercised twice.
- `scripts/cli-crosscheck.sh` gains a `docker create` case with the clone fields (entrypoint,
  env with a sentinel, labels, a fixed and an exposed-only port, a bind and a volume mount, two
  networks with an alias, health, restart with retries, memory and CPU limits) and a
  `docker rename` case, comparing `docker inspect` field by field.
- A read-only real-daemon check derives a spec from a real container's inspect and asserts the
  carried fields equal the source. It sends no create. This is the only real-daemon proof, and
  TESTING says so.

### 13 — One job, and the list updates from events

`JobTitle: "re-create container"`, `JobTarget: <name>`, one job with four steps. The fake and a
real daemon emit create, rename and start events; the desk's existing event-driven refresh
updates the containers list. No bespoke refresh is needed. The tab's per-container state resets
on the new ID, so the new row's Changes tab reads fresh and says the tag names its image.

### 14 — Documentation and screens

README's Changes section replaces the "which this desk does not do" sentence, the Keys table
gains `A`, the safety model gains the typed word, and "What it does not do" loses "never
re-creates" and gains the refusal families and the old-container consequence. DECISIONS gets
round nineteen with these entries and their alternatives. TESTING gets the proof table and the
"not proven" line for the mutation. `make e2e` captures the dialog and a refusal at 100 and 80
columns.

## Risks / Trade-offs

- **A Docker setting slips through the closed model.** The clone would silently lose it. →
  The refused families are tested one by one; the pinned moby `HostConfig` is reviewed field by
  field in a task; DECISIONS and TESTING state the limitation (a future field is not detected).
  If the review finds a meaningful field, it joins the carried or refused table before release.
- **The old container remains and confuses compose.** → Labels are carried; the dialog states
  the consequence; the old container appears in the project group, stopped, by its derived name.
- **Environment leakage.** → A sentinel test walks the dialog, effects, job title, target, log,
  status and an injected create error.
- **Tag moves between confirmation and execution.** → The plan-level pin refuses before any
  step; the job fails with zero steps and names the move.
- **Create fails after the rename.** → The job names the derived name; the operator can rename
  the old back or start it. Nothing is deleted.
- **Bind source missing on the host.** → The daemon may create an empty directory. The dialog
  warns that bind sources are not verified.
- **Name collisions and length.** → Derived name validated, truncated, and disambiguated from
  the snapshot; a rename collision is the daemon's 409 and stops the plan.
- **Two desks re-create the same container at once.** → The second stop or rename finds the
  container changed; the dependent steps are skipped and the job reports the daemon's refusal.
- **`ImageFacts` reads twice on the key press.** → About four GETs and no archive; the key is
  deliberate and the read runs in a `tea.Cmd`, so the desk does not block.

## Migration Plan

- No stored state changes, no schema, no configuration. The new options are zero-valued for the
  existing create form, so its request is unchanged; the existing create tests and the
  cross-check must pass untouched as the regression gate.
- Rollout is the binary itself: the key is additive and the README documents it.
- Rollback is reverting the change; no data was created by the feature beyond the containers the
  operator re-creates.
- After the code and tests, archive the change and verify the main specs and README agree.
