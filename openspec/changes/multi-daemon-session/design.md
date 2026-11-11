# Design

## Context

See proposal.md - Why, for motivation, and the specs in `specs/` for the requirements.

Today the program holds exactly one engine. `main.go` resolves one endpoint, checks it with
`eng.Ping`, and passes the engine into `ui.Model` as `eng` (`internal/ui/model.go:139`). About 36
call sites in `internal/ui` use `m.eng`. One `live.Hub` is built from that engine
(`internal/live/hub.go`), and it owns the event ring, the log ring, and the statistics streams.
The `jobs.Manager` is process-wide. The read-only wrapper and the test guard both wrap individual
engines (`internal/engine/readonly.go`, `B3` in docs/DECISIONS.md).

Remote endpoints are reached through one ssh process each (`internal/sshtunnel`), and the
single-connection rule in daemon-connection is per endpoint, so each member carries its own tunnel.

## Goals / Non-Goals

**Goals:**
- Keep the single-daemon code path as the case of a session with one member: no second, parallel
  way of talking to a daemon.
- Make every action's target one daemon, visible at the point of decision.
- Keep background members to one event stream and one ping.

**Non-Goals:**
- Any view that merges data across daemons.
- Per-member read-only flags, or a configuration file.

## Decisions

### D1. A session of members; the focused member's engine is what the panes see

A new `internal/session` package owns an ordered list of members. Each member holds its endpoint,
its engine, its hub, its link state and, for remote endpoints, its tunnel. `ui.Model` keeps `eng`
and `hub` as the focused member's engine and hub, and changes them on focus. The 36 call sites stay
as they are.

*Alternative:* carry a daemon identifier through every call and every job. Rejected: it touches every
call site and every test for a rule that only matters at focus changes.

### D2. Background members run an events-only hub

The hub gets a mode: full for the focused member (lists, statistics, logs, events), and events-only
for the rest (event stream and ping). On focus change the old member's statistics and log streams
are stopped and the new member's lists are refreshed. Each member keeps its own event ring, so
focusing a member shows the events it recorded while unfocused. Only the notice subset (D6) reaches
the status line.

*Alternative:* one shared event ring with a daemon column. Rejected: it is the merged view that was
ruled out, and a busy member can push out a quiet one's history.

### D3. Per-member caches are cleared on focus; no cross-daemon data on screen

Lists, details and any cache keyed by object identifier are per member. Focusing a member clears the
panes' view of the previous member and shows a loading state until its lists answer. The spec forbids
showing the previous daemon's data as current, so the panes must never fall back to it.

*Trade-off:* the first paint after a focus change is slower by one listing round-trip. For a remote
member that is a visible pause; the loading state covers it.

### D4. Jobs record their daemon; removal and job start are checked under one lock

`jobs.Job` gains a `Daemon` field (the endpoint as the operator named it). The job manager stays
process-wide, so Cancel and the Jobs pane are unchanged apart from the label.

Removing a member and starting a job on it race if they are checked separately. Member removal
marks the member as removing and checks for running jobs under the job manager's lock. A job cannot
start on a member marked removing. This makes the rule in the spec true rather than likely.

### D5. The picker reads contexts from the Docker configuration store

`internal/dockerctx` lists the stored contexts (the directory of context metadata under the Docker
configuration directory) and resolves each one with the existing `contextEndpoint`. Entries that do
not resolve are listed as unusable with the reason.

Duplicates are detected on the canonical endpoint: scheme, host, port, socket path and TLS identity.
Two context names that resolve to the same daemon are one member.

### D6. Notices are a fixed, small set

From an unfocused member the status line shows: a container exit (the `die` and `oom` events), and
the member's disconnect and reconnect. Other events are recorded in that member's history only.
The set is kept small on purpose: a notice per event on a busy daemon would hide the one that matters.

### D7. Picker-added ssh members never ask on the terminal

The launch member may ask, because it runs before the workspace takes over the terminal. A member
added from the picker runs while the desk owns the terminal, so ssh runs in batch mode: a passphrase,
password or host-key confirmation makes the member fail with ssh's reason, shown as disconnected.
This is the rule daemon-connection already states for re-establishing a link, applied to adding one.

*Alternative:* suspend the desk to let ssh ask. Rejected: it breaks the rule that nothing is asked on
the terminal while the desk owns it, and it stalls every other member while it waits.

### D8. Read-only is one flag on every engine in the session

`engine.New` already takes `ReadOnly`. The session passes its single flag to every member it creates,
including members added later. The test guard (B3) is unchanged and applies per engine.

### D9. Launch behaviour is unchanged; the picker path never exits

`main.go` keeps its checks for the launch endpoint, including the exit on an unreachable daemon. The
picker path reuses the same engine construction and ping, with the same time bound, but a failure
becomes a disconnected member rather than an exit.

## Risks / Trade-offs

- **Stale data after focus** → D3 clears per-member caches and shows loading; tests switch focus between
  two fake daemons with different containers and assert that no object of the first appears.
- **Job started during removal** → D4 checks and marks under one lock; a test starts a job and removes
  the member concurrently and asserts exactly one of the two wins.
- **Goroutine or ssh process left behind after removal** → removal closes the hub, the engine and the
  tunnel; a test counts goroutines and ssh children after removing a member.
- **Notice noise** → D6 keeps the set small; a background member's events are rate-limited in the status line.
- **Strip does not fit at 80x24** → the layout tests cover 80x24 with several members, and the spec's
  "many members" scenario keeps the focused one visible.
- **Cost of many ssh processes** → each remote member holds one ssh process. No cap in this change;
  it is measured in the tests, and a cap is a follow-up if the numbers need one.

## Migration Plan

No stored state changes, so there is nothing to migrate. A session with one member behaves as the
single-daemon program did, except that the picker is available. Rollback is reverting the change;
nothing it writes outside the process needs cleaning up.

## Open Questions

- The key that opens the picker, and the key that moves focus to another member. The global key switch
  in `internal/ui/keys.go` is the place to check for conflicts when they are chosen. Neither key is a
  requirement in the specs.
- Whether the member count needs a cap. Deferred until the cost is measured in tests.
