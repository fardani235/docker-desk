## Context

The desk's mutation path is: an action key captures the highlighted row → `prepare` takes a
fresh `plan.Snapshot` → a pure constructor returns a `Plan` → the dialog renders it → consent
→ `runPlan` → `plan.Execute` sends exactly `Plan.Steps`, one request each, never forced,
continuing after a failure and reporting `i/n`. Multi-step plans already exist (prunes,
compose projects) with `Listed: true`, and `planBody` already lays a long list out so that it
scrolls between a fixed identity and a fixed prompt. The rule throughout is "the exact objects
are shown before anything is sent". This change adds the gesture that builds such a plan from
objects the operator chooses. Decisions are numbered **S** and recorded in full in
`docs/DECISIONS.md`.

## Goals / Non-Goals

**Goals:**

- A selection gesture in the List pane that follows objects, not positions.
- A set is planned by the single-object rules, so it can never include anything a single
  confirmation would refuse, and nothing selected is left out without being named.
- One plan, one confirmation, one job; `plan.Execute` untouched.
- A set needs more deliberate consent than one object: the count is typed.

**Non-Goals:**

- Engine changes, force, Docker's prune endpoints, new dependencies.
- Compose selection, cross-kind selection, persistence.

## Architecture

```
List pane                        internal/ui                         internal/plan
─────────                        ───────────                         ─────────────
Space / a / Esc ──► Model.marks[kind] map[rowKey]mark   (reconciled in ensureRows)
action key ───────► invoke ─► invokeOn(a, row, ok, markedRows)
                               │ readOnly?            → refuse, nothing sent
                               │ set && setCapable    → prepareSet ─► prepare(build)
                               │ set && single-only   → refuse with reason            plan.Take
                               │ set && prune         → refuse                            │
                               │ no set               → a.run(row) (unchanged)            ▼
                               ▼                                               plan.Set(op, snap, members)
                        planMsg{fromSet, setKind} ─► onPlan ─► dialog{fromSet}   ├ single(op, snap, m) per member
                                                    planBody: steps + Not included│   (Container / ImageRemove /
                                                    prompt "Type remove 7 to …"  │    VolumeRemove / NetworkRemove)
                        consent (exact word) ─► confirmed(d): runPlan; clear marks├ NotApplicable / Blockers → Skipped
                                                    │                             └ steps concatenated, TotalSize summed
                                                    ▼
                                             plan.Execute (unchanged)
```

## Decisions

### S1. `plan.Set` reuses the single-object constructors

`Set(op, snap, []Member)` dispatches each member to `Container`, `ImageRemove`,
`VolumeRemove` or `NetworkRemove` with the same snapshot and appends their steps. A `Member`
carries `ID` (and `Ref` for an image row) for the constructor, and the list's `Name` so that a
vanished object can still be named. Duplicates collapse. Set-capable ops are exactly the
lifecycle ops and the four removals (`SetCapable`); anything else is a programming error, not
a `NotApplicable`.

*Alternative:* a dedicated set planner per kind — rejected: two rule sets drift.

### S2. Exclusions go to `Plan.Skipped`

`NotApplicable` → its reason; a plan with blockers → its first blocker (the "in use by …" or
"is running …" line; the second blocker line is advice). The set plan therefore never has
blockers; it is runnable iff some steps remain. With none, `Nothing` says so and the dialog
still lists every skipped object with its reason.

### S3. Typed tier, word = verb + count of steps

Count of steps rather than of members, because that is the number the headline shows and the
number of requests sent; they differ only in the image case of S4, where each reference is a
listed row. The input limit grows from 16 to 24 characters for words like `unpause 1000`.

### S4. Image rows grouped by image

Rows of one image whose selection covers all of the image's references (two or more) become
one untag per reference with the last carrying `Size`, `Pin` and `MustBeUnused`; if the image
is in use all those rows are skipped with the users named. Otherwise each row is planned
alone; a partial untag is labelled "untag only" in its detail, and the effects explain it.

### S5. Selection state in the UI

`Model.marks[kind]` maps row key → `{row, pos}`. `ensureRows` refreshes each mark from the new
unfiltered rows (so names and states are current and `pos` orders the set) and deletes marks
whose key is gone — only when that kind's list has loaded. `markedRows` returns the set in
list order, including rows the filter hides. The gutter's first cell is the cursor (`▸`), the
second the mark (`✓`, an already-used glyph of unambiguous width); a marked row that is not
highlighted is drawn in the accent tone.

### S6. Where the set is captured and released

`invoke` captures `markedRows` at the key press; the action menu captures it when it opens
(as it already captures the row), so the menu acts on what its title named. `planMsg` and the
dialog carry `fromSet`/`setKind`; `confirmed` clears that kind's selection as `runPlan` starts
the job. Declining keeps it.

### S7. Rendering within the existing layout

`planBody`'s scroll region gains the **Not included** block after the step rows (blank line,
heading, one wrapped line per skip). It is part of `total`, so both layouts — list scrolls
alone, or the whole middle scrolls at small sizes — keep identity, daemon, prompt and input
fixed. The headline's verb table gains started/restarted/paused/unpaused/killed.

## Risks / Trade-offs

- [A set acts on fewer objects than were selected] → the ident line says "N · M not
  included", the headline counts what will be acted on, the typed word carries that number,
  and each left-out object is listed with its reason.
- [Hidden marks act on objects the filter hides] → they are listed in the confirmation; the
  title's count includes them; Esc clears them first.
- [Typed confirmation for reversible set actions is heavier] → accepted (S3): the count is the
  thing to have read.
- [State changes between dialog and execution] → unchanged guarantees: the daemon refuses what
  became in use, name-addressed steps are pinned, `Execute` continues and reports `i/n`.

## Migration Plan

None: additive. No stored state, no flags.

## Open Questions

None blocking. Moving the highlight down after `Space` was considered and rejected (S5);
revisit if operators ask for faster contiguous marking.
