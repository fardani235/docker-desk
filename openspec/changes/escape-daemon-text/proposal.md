# Proposal

## Why

The desk draws a great deal of text it did not choose: a container's name, image, command, working
directory, labels, mount paths, the daemon's error prose, a compose project's name (which comes
from a label). A session may hold a remote engine, and round twenty-seven's threat model says
plainly that a daemon of the session may be hostile.

Today those strings are made harmless in two places, and one of them is missing on several
screens. The lists, the Attention view and the exports pass names through `escape.Name`, which
shows what the bytes are. Details › Info for all five kinds and most dialogs draw the same
strings raw. What keeps those screens safe is the frame guard alone: every line of every frame
passes `guardLine`, which removes everything that takes no cell — cursor movement, erase, OSC,
DCS, device queries, newlines inside a line, C1 controls, bytes that are not UTF-8 — and keeps
colour sequences, because the desk's own styling is made of them.

Keeping colour is not as harmless as the decision record says. `docs/DECISIONS.md` AT16 states
that a surviving colour sequence "can colour text, not move, hide or forge it". Move and forge
are right. Hide is not: SGR 8 is conceal, a foreground equal to the background does the same, and
nothing resets a colour at the end of a line. A label can therefore hide the rest of the line it
appears on, and the next line's label, until the first style reset ends it. That reaches the
Details pane and the confirmation body — the two places where the operator reads before deciding.

There is a second cost. The guard deletes what it catches, so a string that only the guard met is
drawn shorter than the daemon sent, with no sign that anything was removed. `escape.Name` is
reversible and loses no byte; the guard is not.

Round twenty-seven named this debt in its review and left it, because it was already scope that
change did not have. This change pays it, so that the guard is the second defence rather than the
only one, and so that a daemon's text is shown as what it is.

## What Changes

- Every string the desk draws that a daemon chose SHALL be escaped where it becomes a drawn line:
  Details › Info for containers, images, volumes, networks and compose projects; Images ›
  Findings (a path and the evidence beside it); the Events pane (the action, its outcome and the
  object); the action menu and every dialog; the status line, job titles and job output; and the
  plan step descriptions that skip escaping today (container creation, connect and disconnect).
- The frame guard SHALL stay exactly as it is: same rules, same place, last. This change adds a
  defence; it removes none, and it does not weaken the guard to cover a string it should not have
  to catch.
- Escaping SHALL be display-only. The raw bytes SHALL be what is compared, looked up and sent:
  a rename sends the name it was given, a copy sends the container's real path, and a
  confirmation's typed input is compared against the real value, not the escaped one.
- One mapping SHALL be used for the drawn line, the evidence exports and the Files tree, so that
  what is shown and what is written agree, and the mapping remains reversible.
- The decision record SHALL be corrected: what survives the guard is named as colour, attribute
  and conceal, not colour alone, and the limit is stated as "cannot move, invent or forge; can
  hide". The concealed case SHALL have a test that fails against today's code.
- **No** daemon API, engine method, plan, job or dependency is added or changed.

## Capabilities

### New Capabilities

- `terminal-safety`: what the desk draws from a daemon's text cannot act on the terminal. It owns
  the escaping rule at the drawing boundary, the frame guard as the second and last defence, the
  list of sequence classes that must not reach the terminal, the display-only rule for names that
  are also sent back, and the requirement that the drawn text and the exported text agree.

### Modified Capabilities

None. No existing requirement states that a daemon's string is drawn as received, so no existing
capability's behaviour is being changed. `mutation-safety` still requires a confirmation to state
the object's name; it now states it escaped, which is the same requirement met. `details-search`
still matches the drawn text; escaped names are drawn text.

## Impact

- `internal/ui`: the Info builders in `details.go` for all five kinds, `dialog.go` (confirmation
  bodies, action menu, pickers, job output), `format.go` and the status messages, and the two
  action descriptions that build a line from an object's name.
- `internal/plan/execute.go`: the step descriptions that interpolate a name without escaping
  (create, connect, disconnect).
- `internal/fakeengine`: one fixture whose label, compose project and mount path carry an escape
  sequence, so a test can render Details and the dialogs and check the raw frame.
- Tests: rendering over that fixture at two terminal sizes; a mutation still sending the raw name;
  a guard test for the concealed case, which fails today.
- Documentation: the README's limits, where the details of what is drawn are described, and the
  DECISIONS entry (AT16's wording is corrected; the change's own decisions are written there).
- No new dependency. No change to the fake daemon's API surface beyond seed data.
