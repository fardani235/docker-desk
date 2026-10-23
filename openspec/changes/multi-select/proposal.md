## Why

Every action in the desk applies to the one highlighted row. Stopping the five containers of a
test run, or removing the twelve volumes a filter shows, means five or twelve dialogs — or
going back to the CLI, where `docker rm $(docker ps -aq …)` gives none of the desk's
guarantees. The planner is already multi-object: prunes and compose projects are plans of many
steps, shown in full and executed one request each. Only the List pane is single-object.

## What Changes

- **Selection in the List pane.** `Space` toggles the highlighted row into its kind's
  selection; `a` selects every row the filter shows; `Esc` clears the selection, then the
  filter, then leaves the pane. Marked rows carry `✓` in the two-cell gutter and the pane title
  reads `2 Containers (12) - 3 selected`. The selection is keyed by object identity: it
  survives refreshes and drops objects that vanish. Containers, images, volumes and networks
  can be selected; compose projects cannot. `Space` keeps paging in Details and Events.
- **Actions apply to the selection.** With a non-empty selection, start, stop, restart, pause,
  unpause, kill and remove (containers) and remove (images, volumes, networks) apply to every
  selected object. Shell, connect and disconnect are single-object only and refuse with a
  reason, sending nothing; prune refuses too. Pull and create are unaffected.
- **Set plans.** A new `plan.Set` builds one `Listed` plan by calling the existing
  single-object constructor for each member against one fresh snapshot. A member that is not
  applicable, or that the single-object rules would refuse (in use, running, predefined), is
  collected into a new `Plan.Skipped` with its reason instead of aborting the set; the dialog
  shows these under **Not included**. Selecting every reference of an image deletes the image.
- **Set confirmation types the count**: "Type remove 7 to confirm". Every set confirmation is
  typed, reversible actions included. Single-object wording and tiers are unchanged.
- `TotalSize` of a set is the sum of its steps' sizes ("about X"). The selection is cleared
  when the job starts. `plan.Execute` is unchanged.
- At 80×24 the identity, the daemon, the prompt and the input stay on screen; the listed
  objects and those not included scroll.

### Explicitly out of scope

- Changes to `engine.Engine`, new dependencies, a force flag, Docker's prune endpoints.
- Selecting compose projects; selecting across kinds; persisting a selection.
- Set versions of shell, connect, disconnect, pull and create.

## Capabilities

### New Capabilities

- `object-selection`: selecting several objects of a kind in the List pane, applying an action
  to them as one plan, and how such a plan is confirmed, rendered and executed.

### Modified Capabilities

None in requirement terms. `mutation-safety` and `workspace-layout` (from
`docker-desk-workspace`) are unchanged for single objects; the selection rules are additive.

## Impact

- `internal/plan`: new `set.go` (`Set`, `Member`, `Skip`, `SetCapable`); `Plan.Skipped`.
- `internal/ui`: selection state and keys (`model.go`, `keys.go`, `list.go`), set-capable
  actions and refusals (`actions.go`), set rendering and prompt (`dialog.go`), hints and help
  (`view.go`).
- `internal/fakeengine`: a negative-elapsed guard in the log generator found while testing.
- Docs: README key table and safety model, `docs/DECISIONS.md` S1–S8.
