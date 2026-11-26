# Proposal

## Why

The desk forgets everything when it closes. An operator who works across a local engine and two
remote ones re-adds them with `M` on every launch, re-picks the theme, and re-navigates to the
daemon they were watching. Read-only is worse than forgetful: it is one flag for the whole
session, so protecting a production daemon means making the staging daemon read-only too.
A useful desk should remember the desk the operator arranged, and let caution attach to the
daemon that deserves it.

## What Changes

- Add a small state file under the user configuration directory, mode `0600`, holding the
  theme, each member's endpoint and context name (never credentials or secrets), the focused
  member and each member's read-only flag. It is loaded at launch and can be disabled with
  `--no-state`.
- A corrupt, unreadable or unsafe state file is ignored with one notice; it never prevents the
  launch.
- Restore stored members as background members. An explicit `--host`/`--context` (or the
  existing environment precedence) still chooses the launch daemon; the stored focus is applied
  once that member connects, with a notice. A stored member that is gone or cannot be resolved
  is skipped with a notice.
- Make read-only a property of each member. The strip and the header show the focused member's
  mode; each member's entry in the picker shows its mode. The picker gains a key to toggle the
  selected member. A member added later starts in the session's default mode and can be
  toggled the same way. A toggle takes effect before the next request and sends nothing
  itself.
- Keep `--read-only` as the session-wide lock: every member is read-only, including stored and
  newly added ones, and the picker cannot turn it off.
- Enforce the mode below the UI per member: the engine's read-only wrapper and its HTTP request
  hook follow the member's current mode, so a toggle changes what is refused without
  reconnecting the daemon or dropping its streams.
- Persist changes as they happen: theme, members added or removed, focus moved, and a member's
  mode toggled. Persisting is best effort; a write failure is reported once and never
  interrupts the desk.
- **Behaviour change:** the theme chosen with `T` is now stored, and a stored theme is used when
  neither `--theme` nor `DOCKER_DESK_THEME` is given.

## Capabilities

### New Capabilities

- `session-state`: the state file's location, permissions, contents, when it is read and
  written, what is deliberately not stored, and how a corrupt, unreadable or untrusted file is
  handled.

### Modified Capabilities

- `daemon-session`: every member carries its own read-only mode; the mode is visible in the
  strip, the header and the picker; the picker toggles it; restored members and restored focus
  join a launch under the existing launch rules.
- `daemon-connection`: read-only is a per-member refusal rather than one session flag, while
  `--read-only` remains a session-wide lock; the header names the focused member's mode.
- `colour-themes`: a stored theme is a source for the launch theme, below the flag and the
  environment, and `T` now stores the choice.

## Impact

- `cmd/docker-desk`: `--no-state`; reading and writing the state file around the existing launch
  path; stored theme resolution; passing restored members, modes and focus to the workspace.
- New `internal/state` package: schema, strict parsing, atomic `0600` write, path resolution and
  failure classification, with tests that need no terminal.
- `internal/engine`: the read-only wrapper and HTTP hook become a per-engine mode that can be
  read and set; the two-layer guarantee and the reflection tripwire stay.
- `internal/session`: `Config.ReadOnly` becomes a lock plus a per-member mode; `Add` and
  `AddLaunch` take a mode; members expose and change it; restored members are attached after
  launch. Removal and background-job rules are unchanged.
- `internal/ui`: the focused member's mode replaces the session-wide `readOnly` at every gate;
  the strip, header, help, picker and dialogs name per-member modes; a picker key toggles the
  selected member; theme changes report that they are kept. The image copy gate is evaluated
  against its destination, not the focused member.
- Documentation: README (state file, `--no-state`, per-daemon mode), in-app help, and the
  project's decision records. No new daemon API, no dependency.
