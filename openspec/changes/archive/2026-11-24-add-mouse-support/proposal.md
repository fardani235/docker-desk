# Proposal

## Why

The desk is driven entirely from the keyboard: a click does nothing and the wheel is left to
the terminal, so reaching a row deep in a list or moving to another pane costs the operator
keys that other full-screen tools spend on a pointer. Mouse support is also the only practical
input on terminals used primarily by pointer, where the desk currently offers no way in.

## What Changes

- Enable SGR mouse reporting (cell-motion) at launch, since clicking and the wheel need it;
  `--no-mouse` turns it off and leaves terminal-native selection untouched. Reporting is
  restored after a shell hand-off and switched off when the desk quits.
- A left click acts on the cell it lands on: focus a pane, pick a kind, highlight a list row,
  switch a Details tab, focus a daemon on the strip, highlight a job. Clicking a menu entry or
  a picker row does exactly what Enter does. A click never opens an action menu, never marks a
  row and never sends anything.
- The wheel moves the pane under the pointer without moving focus: three rows per notch in
  List, Details, Events and Jobs; one kind in Kinds; the list or text in Files, Space and
  Repository.
- Editable text rows (list filter, Details search, form fields, repository name, export path)
  take keyboard focus on click, with the caret at the end of their text; a click elsewhere ends
  editing the way Enter does and then applies the click.
- Files, Space and Repository take clicks on their rows and wheel movement in their lists.
- No pointer action confirms a destructive plan or sends a request: typed keyboard consent
  remains the only way to proceed, and clicks on a confirmation do nothing.
- `?`, `--help` and the README document the pointer bindings and `--no-mouse`.

## Capabilities

### New Capabilities

- `mouse-support`: how the desk enables pointer reporting, what each click and wheel notch acts
  on across the desk, dialogs and overlays, how text rows take focus, and the lifecycle that
  restores the terminal.

### Modified Capabilities

- `mutation-safety`: adds the rule that pointer input never mutates the daemon and never
  consents to a plan, alongside the existing keyboard rules.

## Impact

- `cmd/docker-desk/main.go`: the `--no-mouse` flag, the program's mouse startup option, and
  re-enabling reporting after a shell session (Bubble Tea's `RestoreTerminal` does not).
- `internal/ui`: a `tea.MouseMsg` path in `Update`, a hit map captured while rendering, and
  click/wheel handling in the five panes, daemon strip, dialogs, forms, Files, Space and
  Repository.
- Help overlay and hints, README and `--help` text.
- Tests: model-level mouse messages (focus, selection, wheel, dialogs, no mutation) and
  end-to-end SGR sequences on a pseudo-terminal, including `--no-mouse` sending no enable
  sequences and mouse working again after leaving a shell.
- No engine or API changes, no new dependency; bubbletea v1.3.10 already provides
  `tea.MouseMsg`, `tea.WithMouseCellMotion` and `tea.EnableMouseCellMotion`.
