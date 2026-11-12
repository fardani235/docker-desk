# Proposal

## Why

The desk can start, stop, restart, pause, kill, remove, pull and open a shell in a container,
and it can create volumes and networks — but it cannot create a container. A new container
still means leaving the desk for `docker run`, and every action the desk offers presupposes a
container that already exists. Creating one is the missing first step of the lifecycle.

It is also the first plan that *adds* a container rather than acting on or removing one: the
object does not exist when the plan is built, so the confirmation must describe a configuration
instead of naming an object, and later steps of the plan must wait for the container that an
earlier step produced.

## What Changes

- **Create from the Containers kind.** The kind gains a `new…` action (key `n`, also in the
  action menu) that opens a create form. It acts on nothing that exists, so it is offered even
  while a selection is marked.
- **One form, seven things to say.** The form collects the image reference, an optional
  container name, published ports, environment variables, mounts, restart policy and the
  command. Ports, environment and mounts are repeatable rows: `Ctrl+N` adds an entry, `Ctrl+X`
  removes the focused one, `↑`/`↓` moves between entries, and `Tab`/`Shift+Tab` moves between
  fields.
- **The form is the confirmation.** It draws the daemon, the exact configuration being built,
  whether the image is present locally or will be pulled first, and that the container will be
  created *and started*. Enter is the only key that sends anything; Esc sends nothing. A value
  that cannot be parsed or would be refused is reported under the form with the offending field
  focused, and the form stays open.
- **Pull, create, start — one job.** The plan is built from a fresh snapshot. If the image is
  not there it is pulled first; then the container is created; then it is started. All three run
  as one background job, with the pull's progress shown as today.
- **Steps may depend on earlier steps.** This plan is the first whose steps are ordered by
  dependency: create depends on the pull (when there is one) and start depends on create. A
  step that fails causes the steps after it to be skipped and the job to say which were skipped
  and why, instead of attempting them (today every step is attempted independently).
- **A step can produce the identifier the next one uses.** The daemon returns the new
  container's ID on create; the start step addresses it. The plan declares that hand-off rather
  than looking anything up between showing and doing.
- **Read-only refuses it.** The new engine mutation is refused in read-only mode like every
  other mutation, and the action is refused before any form opens.

### Non-goals

- Creating without starting. `docker create` semantics (a container left in `created` state) is
  not offered; start after create is part of the plan.
- Building an image, or creating from a Dockerfile or a compose file.
- Choosing a network, hostname, user, TTY, platform, resource limits, capabilities or any other
  Docker option the seven fields do not cover. The container joins the daemon's default network.
- Attaching to the new container's output, or opening a shell in it automatically.
- Changing how existing containers are acted on, or any of the list, details, events or job
  views.

## Capabilities

### New Capabilities

- `container-creation`: the create form and its fields, the entry point in the Containers kind,
  how repeatable entries and their values are written and validated, how a missing image is
  handled, the pull-create-start plan and job, what the operator sees when it succeeds or fails,
  and the read-only refusal.

### Modified Capabilities

- `mutation-safety`: creation is confirmed by its form rather than by `y` or a typed word, the
  form must state the complete configuration and any download it will perform, and nothing is
  sent until Enter on a form that has been on screen long enough to read.
- `background-jobs`: a plan whose steps depend on one another does not attempt a step whose
  prerequisite failed; the job reports the steps it skipped and why, and a job may carry the
  identifier of the object it produced into a later step.

## Impact

- `internal/engine`: a new mutating method that creates a container from a resolved
  configuration and returns its ID, plus the configuration types the desk accepts. The
  read-only wrapper and its "every mutation is covered" tripwire both grow by one; the fake
  engine gains `POST /containers/create` so the end-to-end and UI suites can exercise it.
- `internal/plan`: a container-create specification value, a new operation, a step payload that
  is not an existing object's identifier, dependency edges between steps, and the carried
  result (the created ID) that the start step addresses.
- `internal/plan/execute.go`: skip a step whose prerequisite did not succeed, record the skip in
  the job, and pass a step's produced ID forward.
- `internal/ui`: a multi-field form with repeatable rows (a new shape for `dlgForm`, which is
  single-input today), the new action and its key/help/footer entries, live validation and the
  configuration summary, and the image-present note.
- Tests: plan tests for parsing, validation and the step graph; engine tests against the fake
  daemon for the request body; UI behaviour tests for the form's keys, settle rule and
  read-only refusal; an end-to-end create.
- Documentation: README (the new key and what it does), `docs/DECISIONS.md` (the form shape,
  create-and-start with an implicit pull, and dependency/result-carrying steps), and a captured
  screen of the form.
