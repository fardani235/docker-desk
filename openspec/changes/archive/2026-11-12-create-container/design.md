# Design

## Context

See `proposal.md` — Why. The constraints that shape the approach:

- A mutation is a `plan.Plan` value built by pure code from a `plan.Snapshot`, drawn by the
  dialog, and executed verbatim by a job (`docs/DECISIONS.md` D11). Every step is one daemon
  request, executed independently: a failure is recorded and the remaining steps still run.
- A form is already a confirmation tier of its own (`TierForm`, D12): submitting it builds a
  plan from a fresh snapshot and starts the job immediately; no second dialog is shown.
- `dlgForm` has one `textinput` and one prompt. `formBody` draws it without height accounting.
- `engine.Engine` wraps `github.com/moby/moby/client`; every mutating method is overridden in
  `internal/engine/readonly.go`, and a reflection test fails if a new mutation is not.
- The UI refuses a `mutating` action before running it when the session is read-only
  (`internal/ui/actions.go:164`).
- The fake daemon has no create endpoint; `POST /containers/create` currently 404s.

## Goals / Non-Goals

**Goals:**

- A create form that is the whole confirmation, and one job that pulls, creates and starts.
- Extend the plan model minimally but honestly: ordered steps that can depend on an earlier
  step's success and address the ID it produced.
- Keep every existing rule: no force, one request per step, read-only below the UI, jobs never
  block the desk.

**Non-Goals:**

- A second confirmation dialog after the form. The form is the confirmation (D12).
- A general workflow engine. Only one dependency edge shape is needed (a chain).
- Editing or restarting with a changed configuration. That is a separate concern.
- Compose-style networks, build, attach, or any option outside the seven fields.

## Decisions

### D23 — The create plan is a chain of three steps with declared dependencies

A create plan is `pull (only when needed) → create → start`. Steps gain two fields:

- `Step.DependsOn []int` — indices of steps that must have succeeded;
- `Step.ProducesID bool` and `Step.IDFrom int` — a create step returns the daemon's ID, and a
  later step takes its `ID` from that result (`IDFrom: -1` means none).

`Execute` keeps an outcome per step. Before attempting a step it checks its dependencies; if any
did not succeed, the step is skipped, logged as `↷ skipped … — <prerequisite> did not succeed`,
and its outcome is "skipped". Skips advance the progress counter, are counted separately in the
final summary, and a job with any failure still ends failed. Existing plans declare no
dependencies and keep today's "one failure does not stop the rest" behaviour.

*Alternatives:* one engine call that pulls, creates and starts (hides three requests, loses the
pull's progress and the created-not-started state); a builder closure that keeps the ID to
itself (not a pure, inspectable plan). Rejected.

*Cost:* `Step` grows, `Execute` grows a dependency check, and the job summary grows a skipped
count. This is the first plan whose steps are not independent, so the rule is stated in
`background-jobs` rather than buried in `Execute`.

### D24 — The spec is a plan-level value; the engine takes its own options type

`plan.ContainerCreateSpec` is what the form parses to and what the plan carries:

```go
type ContainerCreateSpec struct {
    Image   string        // normalized with NormalizeRef
    Name    string        // "" means the daemon names it
    Ports   []PortSpec    // HostIP, HostPort, ContainerPort, Proto
    Env     []string      // "NAME=value", in the order entered
    Mounts  []MountSpec   // Type (volume|bind), Source, Destination, ReadOnly
    Restart RestartSpec   // Name, MaximumRetryCount
    Command []string      // nil means the image's own command
}
```

`Step.Create *ContainerCreateSpec` carries it. The engine gets its own
`engine.ContainerCreateOptions` with the same shape, mapped in `moby.go` to
`container.Config` + `container.HostConfig`: `ExposedPorts` and `PortBindings` (nat types),
`Mounts` (`mount.Mount` with an explicit `volume`/`bind` type), `RestartPolicy`, `Env`, `Cmd`,
`Image`, `Name`. `HostConfig` is left otherwise zero — no force, no privileged, no auto-remove.

*Why the duplication:* the engine's options are the daemon's vocabulary; the plan's spec is the
form's. Keeping them separate stops the UI and the fake daemon from importing the client's
types, which is the rule the package already follows (`docs/DECISIONS.md` D6).

*Why `Mounts`, not `Binds`:* `mount.Mount` states the type the form inferred, so a named volume
and a host path cannot be confused. The daemon still auto-creates a missing named volume.

*Alternative:* reuse `container.HostConfig` in the plan. Rejected: it drags the Moby API into
the pure planning layer.

### D25 — Validation and parsing are plan functions, used by the form

`internal/plan` gains pure parsers and a builder:

- `ParsePort("127.0.0.1:5432:5432/tcp")`, `ParseEnv("NAME=value")`,
  `ParseMount("/srv/data:/data:ro")`, `ParseRestart("on-failure:3")`, and `SplitCommand`
  (moved from `internal/ui/dialog.go`, where the shell form already uses it).
- `ContainerCreate(snap, spec) (Plan, error)` returns the plan or an input error. Errors are
  what the form shows under itself with the offending field focused; `Plan.Blockers` is not used
  for input problems, because a form plan with blockers would be dropped silently (the
  `fromForm` path runs a runnable plan and ignores a non-runnable one).

The parser rules: a port is 1–65535 with tcp default and an optional bracketed IPv6 host
address; env is `NAME=value` with a non-empty name and no whitespace in it; a mount destination
is absolute and a source starting with `/` is a bind; restart is one of the four names, with a
retry count only for `on-failure`; the command uses the existing quote-aware splitter and may be
empty.

Duplicate rules are checked in the builder against the fresh snapshot: an image that will not
normalize, a name already used by a container, a port repeated on the same host address and
protocol. Everything else is the daemon's to refuse, and its message is shown.

*Alternative:* validate in the UI before planning. Rejected: the rules would then be untested
outside a terminal, and the builder must check them again anyway.

### D26 — One form model, and the create form is a list of fields

`dlgForm` is generalised from one input to `fields []formField`:

```go
type formField struct {
    label  string
    kind   fieldKind         // text, list, choice
    rows   []textinput.Model // one for a text field, one per entry for a list
    choice int               // index for a choice field
}
```

Pull, volume, network, shell and create are all one `formKind` each, but the first four are a
form with a single text field; the create form has seven. The focused field is marked, its rows
are editable, and `formBody` renders label/value lines. This avoids two form implementations and
one set of keys.

The restart field is a choice row among `no`, `always`, `unless-stopped` and `on-failure`; when
`on-failure` is chosen, a second row holds the retry limit (empty means no limit), so the field
model stays one shape and `ParseRestart` checks the composed value.

Keys, handled before the input model sees them:

| key | effect |
| --- | --- |
| `Tab` / `Shift+Tab` | next / previous field, wrapping |
| `↑` / `↓` | previous / next row of the focused field (its entries, or the choice and its retry limit) |
| `←` / `→` | cycle the focused choice field |
| `Ctrl+N` | append an entry to the focused list field |
| `Ctrl+X` | remove the focused entry; when it is the last one, clear it instead |
| `Enter` | submit |
| `Esc` | close, sending nothing |

`Ctrl+D` is deliberately not used: the text input already binds it to delete-forward.

*Alternative:* a separate `dlgCreate` dialog. Rejected: two form paths, two key handlers, two
renderers.

*Cost:* the existing four forms are refactored onto the field model; their tests and the shell
form's pre-filled command move with them.

### D27 — The form body scrolls; the summary is part of it

`formBody` currently returns every line it wants and `renderDialog` does not limit its height.
With seven fields and repeatable rows that overflows an 80×24 terminal. `formBody` takes
`maxBody` like the plan and picker bodies and scrolls a window of fields around the focused one,
with a `field i of n` line and the existing hint line pinned at the bottom.

The body carries, above the hint line, a summary built from the values as they are now: the
daemon, the normalized image reference and whether the last image list has it (so it says
"present" or "will be pulled first" before Enter), the name or that the daemon will assign one,
each port as `host → container/proto`, each mount as `source → destination (volume|bind, ro)`,
the restart policy, and the command. Environment entries are drawn as `NAME=value`; nothing
else ever repeats the value.

The summary is display-only and may be a refresh behind. On Enter the plan is built from a fresh
snapshot, which decides the pull and catches anything that changed.

### D28 — The engine method, the read-only tripwire and the fake daemon

`Engine` gains:

```go
CreateContainer(ctx context.Context, opts ContainerCreateOptions) (id string, err error)
```

It maps to `cli.ContainerCreate` and returns `res.ID`. `readonly.go` gains the override, and the
hard-coded `checked != 18` in `readonly_test.go` becomes 19 — the failure is the reminder that
the wrapper must be updated.

The fake daemon gains `POST /containers/create` (two segments, currently only `DELETE` is
routed):

- decode `{Image, Cmd, Env, ExposedPorts}` and `HostConfig{PortBindings, Mounts, RestartPolicy,
  Binds}`;
- 404 `No such image: <ref>` when the image is absent (the fake already holds image refs);
- 409 when the name is taken, mirroring the daemon;
- append a container in `created` state with the spec's fields, emit `container`/`create`, and
  answer 201 `{"Id", "Warnings"}`;
- the existing `POST /containers/{id}/start` then runs it, so the desk's event-driven refresh
  and the start step are exercised without new fake machinery.

*Alternative:* implement `Binds` only, as a string list. Rejected: the fake would then
re-implement Docker's `source:destination:ro` parsing to classify mounts, duplicating what the
plan parser already decided.

### D29 — Only the form and the job see the environment values

The plan's step description (what the job logs) is `create container <name> from <image>`,
never the spec. The job title is `create container` with the name, or the image when the daemon
will assign the name; after the create succeeds the output adds the assigned name and the short
ID. Environment values live only in the form body and in the request.

*Why:* the desk already treats environment values as values that do not leave the image detail
method (`internal/engine/engine.go`). A job's output is read, copied and pasted into issues; it
must not become a leak.

### D30 — After a successful start, nothing special is needed

The daemon emits `container` events for create and start; the existing live hub marks the list
dirty, the debounced refresh picks the new row up, and the detail pane follows the highlight.
No post-job refresh is added. The e2e test asserts the row appears running.

## Risks / Trade-offs

- **Form height at 80×24** → the scrolling window in D27 keeps the focused field and the hint
  line visible; a layout test pins the size.
- **`Ctrl+N` / `Ctrl+X` are unfamiliar** → the form's hint line and the in-app help list them;
  `Ctrl+D` stays with the text input.
- **The pull in the plan can surprise** → the summary states it before Enter, and the plan is
  built from a fresh snapshot, so an image pulled meanwhile removes the step.
- **A cancel or start failure between create and start leaves a `created` container** → the job
  says so and names it; the container is a normal object the operator can start or remove.
- **A dependency failure makes a job "failed with skipped steps"** → the summary counts them
  separately; the `background-jobs` delta states it.
- **A long environment value with a comma** is fine (one value per row), unlike a comma-separated
  field; this is why the form chose repeatable rows.
- **Ports on IPv6** are accepted only bracketed (`[::1]:8080:80`); an unbracketed address is
  rejected with that hint rather than mis-split.
- **The refactor of the four existing forms** touches their tests; they are single-field cases
  of the same model, and the behaviour (Enter submits, Esc cancels, busy state) is unchanged.

## Migration Plan

No stored state, configuration or wire format changes: the change adds one method, one action
and one dialog shape. Rolling back is a revert; containers created by the feature are ordinary
containers and keep running.
