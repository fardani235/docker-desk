# Proposal

## Why

The desk can hold several daemons at once, but it has no single answer to the question an
operator actually asks first: **is anything wrong anywhere?** A failing healthcheck is visible
only inside the daemon's STATUS prose; a crash loop, an OOM kill and an exit code live in the
highlighted container's Info tab, its logs and the Events pane. Unfocused daemons report a
`die` or `oom` as a transient notice, never their current state. So the operator cycles through
daemons and containers, one at a time, to find out what needs a person.

## What Changes

- Add a full-screen **Attention** view (`H`): every connected daemon of the session in one
  screen, problems first, each block naming its endpoint, engine and the time it was read.
- Classify containers with one ladder: **needs you** (restarting, unhealthy, dead, exited
  non-zero with OOM named), **watch** (running but restarted repeatedly, created but never
  started), then counts of **fine**, **unknown** (no healthcheck, still starting), **not a
  problem** (paused, exited 0).
- Read once when the view opens and on `r`; while it is open, re-read a daemon only when its
  already-streamed events say something changed. No polling.
- Inspect only the candidates that need a reason (exit code, OOM, restart count), progressively
  and with a bound, and say what was not inspected.
- Enter jumps to the container on the desk, focusing its daemon and selecting it; Esc restores
  the pane, kind and highlight exactly as they were. The view is read-only and has no actions.
- Make attention visible without the view: health in the Containers STATE cell, and a count of
  the certain cheap signals (dead, restarting, unhealthy) that opens the view.
- Allow an explicit open view to read other members' lists, a narrow exception to the rule that
  unfocused daemons keep no list refreshes. Background members stay quiet otherwise.

## Capabilities

### New Capabilities

- `container-attention`: what needs attention, how it is classified, how the session-wide view
  reads and refreshes its daemons, how rows are ordered and counted, how a jump works, and what
  the view refuses to claim.

### Modified Capabilities

- `daemon-session`: the unfocused-daemon rule gains one exception — an explicitly opened view
  may read a member's lists, bounded to that open view, without turning on background refreshes.

## Impact

- `internal/ui`: the new full-screen view (patterned on Space/Files/Repository), the Containers
  state cell and attention count, key handling, jump and restore, help and hints, responsive
  rendering, mouse hits.
- A small pure package for classification, ordering and counts, so the rules are testable
  without a terminal.
- `internal/session`/`internal/live`: reading another member's containers and knowing when its
  event stream says to re-read; cancellation when the view closes.
- `internal/engine`: no new methods — the existing container list and inspect are enough.
- Documentation: README's keys and a new Attention section, the in-app help, and the project's
  decision/testing records. No daemon-side state, no configuration or flag changes.
