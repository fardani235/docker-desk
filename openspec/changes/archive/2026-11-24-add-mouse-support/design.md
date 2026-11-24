# Design

## Context

The desk is a Bubble Tea v1 program (`bubbletea v1.3.10`) drawn as five panes, a header, a
daemon strip and a footer. The program is created in `cmd/docker-desk/main.go` with
`tea.WithAltScreen()` only, so no mouse mode is requested and `tea.MouseMsg` never arrives.

Three constraints shape the approach:

- **The screen is computed while drawing.** Pane rectangles come from `computeLayout`, but the
  rows actually on screen — list offsets, event scrollback, job order, Files/Space/Repository
  rows — are decided inside the render functions. `View` returns the cached frame unchanged
  when nothing is dirty, so geometry cannot be recomputed later from state without duplicating
  eight renderers.
- **Keys are the only input path today.** `onKey` routes in a fixed order: dialog, list filter,
  Details search, Packages, Files, Space, Repository, global keys, then the focused pane. A
  pointer path must not accidentally behave like a key (a wheel must not clear a pending plan
  or type into a filter).
- **The terminal is handed over for a shell.** `tea.Exec` calls `ReleaseTerminal`, whose
  `restoreTerminalState` disables mouse reporting; `RestoreTerminal` re-enables the alternate
  screen, bracketed paste and focus reporting but **not** the mouse. The desk has to ask for it
  again itself (see `internal/actions.go`, `shellDoneMsg`).

See `proposal.md` for motivation and the two spec deltas for the required behavior.

## Goals / Non-Goals

**Goals:**

- Every click and wheel notch acts on exactly what the most recent frame drew at that cell.
- Clicking and scrolling never change what the desk is allowed to send; the keyboard remains a
  complete alternative and the only way to consent.
- One pointer path for the whole program, not one per view, and no new dependency.
- `--no-mouse` is a complete escape hatch: no enable sequences, no handling, native selection
  unchanged.

**Non-Goals:**

- Drag selection, double-click, right-click menus, scrollbars, hover states, or touch.
- Caret placement *inside* a text value; a click puts the caret at the end.
- Reordering, resizing or moving panes with the pointer.
- Any mouse behavior for the report/export modes (`--report`, `--check`), which never open the
  desk.

## Decisions

### D1 — The frame records its own hit map

Each fresh render builds a list of clickable regions in absolute terminal cells (0-based, the
same coordinates `tea.MouseMsg` carries). A hit is a rectangle plus what it means:

```go
type hit struct {
    rect           rect
    kind           hitKind // pane, kindRow, listRow, detailsTab, daemon, job,
                           // eventRow, menuRow, pickerRow, formRow, textRow,
                           // filesRow, spaceRow, repoRow, dialogText
    pane           Pane
    index          int
    key            string // object identity where one exists
}
```

The list is built into a local slice and assigned to the model at the end of the frame, so a
half-drawn frame can never be clicked. More specific hits are appended after their pane and the
event path scans the list backwards, so a row beats its pane and a dialog beats the desk.

Rendering already knows the offsets and row identities, so each draw site adds its hits as it
draws: the box helper records the pane rectangle at the layout rect, list/kind/event/job
renderers record visible rows, the Details title records tab spans, `renderStrip` records
members, the overlay boxes record their rows, and `renderDialog` records menu/picker/form rows.

**Alternatives.** Recomputing geometry at event time duplicates each renderer's offset and
visibility logic and drifts from what is on screen. Parsing the ANSI frame back into cells is
fragile (styles, wide runes, box borders) and slower than drawing. A hit map captured by the
draw is the only one that is true by construction.

The map is cleared when a resize or quit arrives, so a click between the resize and the repaint
cannot use old coordinates. Every applied hit is revalidated against current state (kind still
exists, row key still in the list, tab still in range), because a queued state change may have
arrived after the frame was drawn.

### D2 — One pointer entry point, gated before dispatch

`update` gains a `case tea.MouseMsg` handled after frame and size messages:

1. return immediately if `opts.NoMouse`, the terminal is too small, no daemon is focused, no
   frame has been drawn, or the event is not a left press or a wheel notch (motion, release,
   right button and middle button do nothing);
2. resolve `(X, Y)` against the hit map (no hit: only a pane hit can come from the frame, so no
   hit means nothing happens);
3. dispatch in the same precedence `onKey` uses: an open dialog, then Packages, Files, Space,
   Repository, then the desk.

A wheel event is `MouseActionPress` with a wheel button; one notch is one event. The model does
not synthesize key messages: wheel movement calls the same small movement helpers the keys call
(`moveSelection`, `scrollLogs`, `textView.scroll`, `readBack`, `setKind`, `moveJob`, the overlay
cursor movers), so a wheel cannot trip "a key means take over" side effects such as dropping a
preparing plan or ending a filter. Where a helper does not exist, the key handler is split into
a shared mover.

**Alternatives.** Synthesizing `tea.KeyMsg` from clicks keeps one code path but makes a pointer
event capable of everything a key is, including `y` on a confirmation; that is exactly the
safety line the change must not cross. A separate handler per pane spreads the precedence rules
across the codebase and will disagree with `onKey` over overlays.

### D3 — Click semantics mirror the keyboard's navigation, never its actions

| Click lands on | Effect |
| --- | --- |
| Pane border or blank area | Focus that pane; selection and scroll unchanged |
| Kinds row | Focus Kinds, `setKind` |
| List row | Focus List, `selectIndex` (details settle, marks untouched) |
| Details tab | Focus Details, set `m.tab[kind]` directly (not `cycleTab`) |
| Daemon strip entry | `focusMember(id)` |
| Jobs row | Focus Jobs, `jobSel = job.ID` |
| Events row | Hold the ring and put the event cursor on that row |
| Files/Space/Repository row | Set that view's cursor, focus stays in the overlay |
| Menu entry | `invokeOn` exactly as Enter |
| Picker/daemons row | Move the row selection; a second click *or* Enter chooses |
| Form row | Focus the field, caret at end |
| Confirmation | Nothing |

A single click never calls `openMenu`, `toggleMark`, `startFilesRead`, `startDiskUsage`,
`repoPull`, or any action. Hovering does nothing.

Clicking a row while a filter or search is being typed ends the edit keeping its text (the
same result as Enter for a filter/search), then applies the click; for the Repository name it
keeps the text without asking the registry, which Enter would. That distinction is recorded in
the spec's "click outside a text row" requirement.

### D4 — The wheel moves what is under the pointer, and focus stays put

One notch is three rows in List, Details, Events and Jobs; one kind in Kinds; three rows in the
Files, Space and Repository lists. The wheel over Details scrolls whatever tab is current
(logs stop following on an upward notch); the wheel over Events behaves as `readBack` upward
and resumes following when a downward notch is already at the newest entry. The wheel over a
dialog scrolls its body (a confirmation's object list, help and job text). The wheel never
moves keyboard focus, so an operator can scroll logs while the List keeps the keys.

**Alternative.** Focus follows the wheel, as some terminals do. It makes casual scrolling
silently change where the next action key applies, which is worse for a control plane.

### D5 — Text rows take focus, not text

Clicking a text row (list filter, Details search, form row, repository name, export path) sets
its owner's editing flag and calls the input's `CursorEnd()`. There is no per-character caret
hit test: the visible text of a long value scrolls horizontally, and mapping cells back through
the input's unexported offset is fragile. Arrow keys move from the end. Clicking outside any
text row finishes the edit without submitting a form or asking the registry.

### D6 — Dialogs are clickable where choosing is safe, inert where it is not

Menu entries invoke their action exactly as Enter does; that can open a plan or form but never
sends. Picker, daemons and job-viewer rows move their selections; picker rows choose on a second
click, matching Enter. Help and job text only scrolls. A confirmation records no interactive hit at all, so clicking its
prompt, its `y` hint or its typed word cannot consent; only the wheel scrolls its list. The
existing `ArmDelay` still applies to whatever the keyboard does next.

This keeps `mutation-safety` structural rather than a check scattered through handlers: there
is no code path from a `MouseMsg` to `confirmed()` or `submitForm()`.

### D7 — Enablement is an option with an off switch, not a constant

`ui.Options` gains `NoMouse bool` (zero value = enabled, so every existing test bench and any
library caller gets the behavior without opting in). `main` adds `--no-mouse`, passes it
through, and appends `tea.WithMouseCellMotion()` to the program options only when it is false.
After `onShellDone`, `Update` batches a command returning `tea.EnableMouseCellMotion()` so the
desk's mouse mode is restored after `Exec`; `--no-mouse` sends nothing.

Cell-motion (1002) rather than all-motion (1003): motion is ignored anyway, and all-motion
floods the queue while the pointer merely moves. SGR coordinates (`1006`) come with the option.

### D8 — Help, usage and the README say what the mouse costs

The help overlay gains a pointer section (click focuses/selects, wheel scrolls, `--no-mouse`),
and the footer hints keep their keyboard-only form so they do not grow. `--help` and the README
flag table describe `--no-mouse` and the Shift/terminal-override note for native selection. The
README's opening "all from the keyboard" line becomes "keyboard or mouse".

## Risks / Trade-offs

- **[Native text selection is lost by default]** → Documented in `--help` and README, one flag
  away, and terminals offer Shift as the standard override. The desk never hides the mouse
  when `--no-mouse` is set.
- **[Stale hits after a state change]** → The map is rebuilt by the next render, and every hit
  is revalidated (row key exists, index clamped) before it is applied. A resize clears it.
- **[A click that chooses a picker row connects a daemon]** → Choosing is the same effect as
  Enter and sends only reads; the picker still shows what it will connect to.
- **[Wheel over an unfocused pane changes state the operator was not looking at]** → The
  changed pane redraws with its cursor visible, and focus is untouched, so the next key still
  applies where it did. Three-row steps make accidental large moves unlikely.
- **[Bubble Tea stops reporting after a shell]** → `onShellDone` re-enables it; an end-to-end
  test clicks after leaving a shell.
- **[Click coordinates differ by 1 between SGR and Bubble Tea]** → Model tests construct
  `tea.MouseMsg` with 0-based cells; pty tests send 1-based SGR sequences and assert on screen
  state, not on coordinates.

## Migration Plan

No stored data or API changes. `--no-mouse` restores today's terminal behavior for anyone who
prefers it; reverting the change removes the flag and the handler. The default terminal
behavior changes for existing users (selection now needs Shift), so the release note and README
call that out.
