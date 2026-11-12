# Proposal

## Why

The desk talks to exactly one Docker daemon for its whole lifetime, and it cannot switch while
running. Operators who manage several daemons (production, staging, a laptop, a remote host over
ssh) must quit and relaunch with a different context to look at another one, and while they look
at one they have no sight of the others: a container that dies on staging, or a pull still running
on production, goes unnoticed. This change lets one session hold several daemons, with one of them
focused at a time and the others kept under watch.

## What Changes

- **Several daemons in one session.** The daemon chosen at launch (by the current precedence:
  `--context`, `--host`, `DOCKER_HOST`, the context store, then default) is joined by any number of
  daemons added from a picker of the Docker contexts on this machine. Daemons are added and removed
  while the desk is open.
- **One focused daemon.** The panes, actions, Events history, statistics and logs all belong to the
  focused daemon. Focus moves with a key and the strip shows which daemon has it.
- **Background daemons stay connected.** Each connected daemon that is not focused keeps a ping and
  an event stream. Its container exits, disconnects and reconnects appear as notices in the status
  line, tagged with the daemon's name. Lists, statistics and logs are not streamed for it; they load
  when it is focused.
- **Read-only is one flag for the session.** `--read-only` applies to every daemon, including ones
  added later.
- **Jobs belong to a daemon.** A job keeps running on its daemon when focus moves elsewhere and is
  listed with its daemon. A daemon with running jobs cannot be removed until they finish or are
  cancelled.
- **Removal is allowed for any daemon,** including the launch daemon. When the last daemon is
  removed the desk stays open on an empty state with the picker one key away.
- **The launch daemon must answer before the workspace opens,** as today. Daemons added later may be
  down: they are shown as disconnected, with the reason, and retried.
- **BREAKING (behaviour):** the guarantees "exactly one daemon for the whole lifetime" and "no way
  to switch daemon while running" end. The archived `remote-daemons-and-themes` proposal listed
  "More than one daemon per session" as out of scope; this change reverses that line. Scripts that
  relied on the program refusing to act on anything but one fixed daemon are not affected by the
  launch rule, which is unchanged, but the picker can now add a second daemon during a session.

### Non-goals

- A merged view across daemons, and any action that spans daemons. Every action has one target.
- A per-daemon read-only flag.
- A configuration file, or saving the session between runs.
- Copying images, volumes or containers between daemons.
- Changing how a single daemon is reached: endpoint parsing, TLS, ssh and the transport rules are
  unchanged.

## Capabilities

### New Capabilities

- `daemon-session`: the set of daemons connected in one session, which one is focused, adding and
  removing daemons from the picker, background liveness and notices, the rules for removal, and
  the empty state.

### Modified Capabilities

- `daemon-connection`: the single-daemon and no-switching requirement, the launch-only rule for
  "unreachable before the workspace opens", the identity header (now the focused daemon and the
  strip), connection loss (per daemon), and read-only (session-wide).
- `engine-events`: the Events pane follows the focused daemon; events from background daemons are
  notices, not history.
- `background-jobs`: jobs record their daemon, keep running across focus changes, and block removal
  of their daemon while they run.
- `live-stream-responsiveness`: statistics and log streams end when their daemon loses focus, and
  background daemons hold only the event stream and the ping.
- `workspace-layout`: a daemon strip is on screen at the standard size, and its keys are reachable
  from the keyboard.

## Impact

- `cmd/docker-desk/main.go`: launch still resolves one endpoint and checks it; the session holds a
  set of engines instead of one.
- `internal/ui`: the single engine field becomes the focused daemon; header, strip, picker,
  confirmations, Events and job views carry the daemon.
- `internal/live`: one hub per connected daemon, with stream limits applied across the session.
- `internal/dockerctx`: listing the contexts in the Docker configuration store for the picker.
- `internal/jobs`: each job records its daemon.
- `internal/engine`: the test guard and read-only wrapper apply per engine; no change to their rules.
- Tests: the fake daemon must run several instances at once; the end-to-end suite gains a
  multi-daemon scenario.
- Documentation: `docs/DECISIONS.md` entries B20 and R7 are superseded; README and the operator
  guide describe the picker and focus keys.
