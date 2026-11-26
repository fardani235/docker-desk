# Design

## Context

See `proposal.md` - Why. What shapes the approach is where the raw bytes already live and where
they are still needed.

A daemon's value enters the desk in `internal/engine`'s decoded types (`container`, `image`,
`volume`, `network`, `container detail`, `event`), reaches the UI through `snap` and the
Details pane's own `detail` structs, and reaches a mutation through `internal/plan`, which reads
a fresh snapshot and builds the request from identifiers, not from drawn text. Escaping at the
engine boundary would therefore escape values that are sent back — a rename target, a copy
path, a lookup by name — and would double every backslash on the way out.

Three mechanisms exist today, and none covers the gap:

| Mechanism | What it does | Where it stops |
| --- | --- | --- |
| `escape.Name` | reversible mapping: a control byte becomes `\xNN`, a backslash `\\`, a reordering character `\uNNNN` | applied in lists, Attention, the Files tree, most plan steps, the exports |
| the frame guard | removes every sequence that takes no cell, keeps SGR | last line; keeps colour, so conceal passes |
| `files.ReadPreview`'s `inertLine` | makes file contents inert, keeps backslashes | contents only, by design |

The gap is the views that read raw engine types and compose their own lines: Details › Info for
all five kinds, the confirmation body (`internal/ui/dialog.go:604` draws `s.Name` and
`s.Detail`), the Events pane (`internal/ui/panes.go:53` builds the line from the event's action,
name and attributes), Findings (`internal/ui/findings.go:552` draws `f.Path` and the evidence),
and the plan's step descriptions (`internal/plan/execute.go:233`, `:256`, `:258`).

## Goals / Non-Goals

**Goals:**

- One rule that a reviewer can check in a diff: a value is escaped when it becomes text.
- The same `escape.Name` mapping the lists and exports already use, so nothing shows one string
  and writes another.
- A test that fails today for each view and each dialog, and that keeps failing when a new
  drawing site is added without escaping.

**Non-Goals:**

- Changing the frame guard's rules. It stays as it is, with one bounded addition (D7).
- Escaping file contents or log lines. They are not names; `inertLine` and the log stream's own
  treatment stay. Escaping them would show a file's bytes as something other than what they are.
- Escaping anything that is sent to the daemon or compared with what it reported.
- A new dependency, a new package, or a new command-line flag.

## Decisions

### D1 — Escape at the drawing boundary; keep the guard behind it

The escaping happens where a value becomes part of a line that will be drawn. The guard is not
weakened and not removed: it remains the only mechanism that must hold when a site is missed.
This keeps the desk's two independent defences, the same model read-only mode follows.

| Considered | Why not |
| --- | --- |
| Escape in `engine`'s decoders | the same values are sent back in requests and matched against the daemon's answers; a doubled backslash breaks a path |
| Escape inside `guardLine` | the guard cannot tell the daemon's sequence from the desk's own, and deleting cannot show what the value contained |
| Drop SGR from the guard | kills every colour and attribute the desk draws |
| Narrow the guard to a palette allow-list | themes are configurable, and lipgloss picks the profile; brittle and still incomplete |

### D2 — Escape when a value becomes text, never when it becomes a request

A name keeps its raw bytes in the engine types, the snapshot, the plan's steps and the
confirmation's data. It is escaped exactly where it is interpolated into a string that the
operator will see. This is what lets one struct carry both copies of the truth.

The line is drawn by purpose, not by type: `plan.Step.Name` is a display field (the step's
request uses `ID`), so the plan escapes it when it composes a description; `Plan.Word` is
compared with what the operator types and is sent nowhere, so it stays raw.

### D3 — One escape per value, owned by the layer that composes the string

`escape.Name` is not idempotent: a backslash becomes two. Applying it twice is visible (`\\`)
and is the most likely mistake this change can make. So each layer owns a value once:

- the UI escapes values it reads from engine types;
- `plan` escapes the display strings it composes — step descriptions, the target and job names,
  effects, warnings, blockers, skipped entries, the refusal messages built with `na(...)`, and
  the log lines `execute` writes;
- the dialog draws a plan's strings as they are and escapes nothing from them a second time.

A test asserts a hostile label is drawn once-escaped, so a double escape fails it.

### D4 — Escape the daemon's part, then apply the tone

Values in the Details pane are often styled: `st.tone(t, escape.Name(x))`, never
`escape.Name(st.tone(t, x))`. Escaping a styled string would escape the desk's own colour
sequences and draw them as text. Where a line mixes a static prefix with a daemon value, each
part is composed on its own and joined.

### D5 — What counts as untrusted

Escaped: container names, image references, commands, working directories, users, labels and
their keys, mount sources and destinations, port host addresses, daemon error prose, compose
project and service names, volume and network names and their labels, subnets and gateways,
registry and engine names, event actions, outcomes, names and attributes, findings paths and
evidence, archive paths (already done in the Files tree), and operator input the desk draws back.

Not escaped: file contents (the reader), log lines (their own treatment), and the desk's own
text. Log lines are contents a person asked to read; a newline in them is already handled by the
line splitter, and the guard removes what acts.

### D6 — Meaning is computed from raw bytes

Any decision derived from a value — `stateTone`, `eventTone`, the action the event line uses to
choose its columns, a match in search — reads the raw form. Escaping first would change a switch
on `"die"` into a switch on `"d\x1bie"`.

### D7 — The guard stays, and every line ends with a reset

The guard keep-rules do not change. One addition: after guarding, every line that contains a
colour sequence is terminated with SGR reset. This bounds the damage of a missed escape to its
own line instead of carrying conceal or colour into the next one. It costs four bytes on a
line that already ends with a reset in the common case, and changes no cell's width.

Rejected alternative: making every drawn value a typed wrapper that escapes at construction.
It would make a missed site a compile error, which is stronger, but it rewrites every `kv`,
`parts` slice and dialog body in the UI, and the fixture sweep below already catches a missed
site in every view the fixture reaches.

### D8 — One fixture, every category, not in the sandbox seed

The fake engine gains one seeded container for tests whose fields each carry a different hostile
value: an SGR conceal in a label, a newline in a mount destination, a C1 control in the compose
project label, invalid UTF-8 in the working directory, a right-to-left override in the command,
and an escape in a volume and a network label. It is a test fixture, not part of the demo seed,
so the captured screens stay as they are.

### D9 — What proves it

- Per view and per dialog: render with the hostile fixture at two terminal sizes, assert the
  frame contains the literal escaped text (`\x1b[8m` as characters) and does not contain the
  sequence's bytes, and assert the line widths are the ones the layout produced.
- Per mutation: a copy-in to a path with a backslash sends one backslash; renaming to a typed
  name sends exactly that name; stopping a project whose label carries a control byte finds its
  containers.
- Per guard: T5's no-colour case, and the reset that ends a concealed line.
- The existing `TestGuardLeavesTheDesksOwnFramesAlone` keeps the desk's own frames unchanged
  apart from the line reset, which is asserted deliberately rather than tolerated.

### D10 — The record

`docs/DECISIONS.md` AT16 says a surviving colour sequence "can colour text, not move, hide or
forge it". That is corrected: it cannot move or invent, but SGR 8 conceals and a matching
foreground hides, which is why this change exists. The change's own decisions are recorded
against the round that carries it, and README's limits section says what is now guaranteed.

## Risks / Trade-offs

- **A missed site.** The fixture sweep is the mitigation, the per-line reset bounds the damage
  to one line, and the guard still removes everything that acts. A site the fixture does not
  reach is not proven safe.
- **A double escape.** Visible as `\\` and caught by the once-escaped assertion. The rule in D3
  is the defence; a reviewer should reject any hunk that escapes a plan-composed string.
- **Screen changes.** Escaping changes nothing for a clean value, because `escape.Name` returns
  the string unchanged after a scan. Captured screens change only if the demo seed changes, and
  D8 keeps it unchanged.
- **Search and filter.** A query matches the drawn text, so a name now containing `\x1b` is
  found by typing that text. That is the same rule the Details search already states.
- **Cost.** `escape.Name` scans once and allocates only when it must; the Details pane redraws
  on highlight and on events, not per frame, so the added work is not in a hot path.

## Migration Plan

Not applicable: a single binary with no stored state. The change ships with the next build.
