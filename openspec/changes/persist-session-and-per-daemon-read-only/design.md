# Design

## Context

See `proposal.md` for motivation and the spec deltas for the required behaviour.

Today the launch path in `cmd/docker-desk/main.go` resolves one endpoint, builds one engine
with `engine.Options.ReadOnly`, verifies it and hands it to `ui.New`. The UI creates
`session.Config{ReadOnly: …}`, which forces `DialOptions.ReadOnly` for every later member and
sets one `Model.readOnly` flag that 76 UI sites consult. Read-only is therefore enforced in
three places that all share one boolean: the session config, the engine wrapper
(`engine.readOnlyEngine`), and the HTTP request hook installed by `engine.New`. The theme has no
memory, members and focus live only in the running session, and no local state is read or
written anywhere.

The relevant existing decisions are D17 (read-only is enforced below the UI twice), M8
(read-only is one flag on every engine of the session) and the theme rule T that `T` never
stores the choice. This change modifies all three.

## Goals / Non-Goals

**Goals:**

- One state file with strict parsing, safe permissions and no secrets, loaded at launch and
  written as the desk changes.
- Read-only as a property of a member, enforced at the same depth as today (wrapper plus HTTP
  hook), changeable without reconnecting the daemon.
- Restoring members and focus without ever changing which daemon the launch rules select, and
  without asking anything on the terminal.
- A stored theme that participates in the existing precedence without weakening launch
  validation.

**Non-Goals:**

- Credential storage, state sharing between machines, profiles or named desks.
- Restoring scroll positions, filters, selections, open views or jobs.
- Changing what counts as a mutation or how confirmations work.
- Changing `--no-registry`, `--no-compose-files` or `--no-mouse`.
- Per-member `--read-only` command-line syntax; the flag stays one switch for the run.

## Decisions

### D1 — One JSON file, versioned, with an explicit allow-list of fields

`internal/state` owns the schema:

```json
{
  "version": 1,
  "theme": "nord",
  "focus": "ssh:ops@prod-1:/var/run/docker.sock",
  "members": [
    { "endpoint": "ssh://ops@prod-1/var/run/docker.sock",
      "context": "prod", "readOnly": true }
  ]
}
```

A member stores only the endpoint the operator selected and its context name; the per-member
`readOnly` is the operator's intended mode, not a forced lock. Nothing else is stored — no
`DOCKER_HOST` value, no TLS file names, no ssh command, no jobs, no selected object. Decoding is
strict: unknown fields, a version other than 1, a member entry missing its endpoint, or a field
of the wrong type rejects the whole file rather than applying part of it. The alternative —
best-effort partial loading — was rejected because a half-restored session is harder to explain
than a clean start.

### D2 — Path, ownership and permissions are part of loading

The file is `<os.UserConfigDir()>/docker-desk/state.json` (`$XDG_CONFIG_HOME` when set, otherwise
`~/.config`). The directory is created `0700`; the file is written `0600`. Loading uses `Lstat`
and accepts only a regular file, not a symlink, owned by the current user and not writable by
group or others. Anything else is ignored with one notice. The alternative — following a
symlink so that dotfile managers keep working — was rejected: the state file can add daemons to
a session, so its identity is part of the trust boundary, and a symlink makes the target
unpredictable. A user who wants another location can use `--no-state` or a wrapper that copies
the file.

Writes go to a randomly named temporary file in the same directory, opened `O_CREATE|O_EXCL`
with `0600`, are `fsync`ed, and are renamed over the old path. A symlink already at the path is
replaced, not followed.

### D3 — A file that fails to load also disables saving for the run

Any load failure — missing is not a failure; unreadable, unsafe, malformed, unknown version or
wrong type is — produces exactly one notice and a `Store` with saving disabled. The desk opens
with no restored state and does not overwrite what it could not understand. The notice is a
status line once the workspace is open, or stderr in `--check`/`--report`, so a corrupt file
never changes the launch result. The alternative — write defaults over the bad file
immediately — was rejected because it destroys the evidence the operator needs to fix it.

### D4 — Read-only is a mode inside the existing two-layer engine, not a rebuild

`Engine` gains `ReadOnly() bool` and `SetReadOnly(bool)`. `engine.New` always returns the guarded
engine; the wrapper and the HTTP hook share one `*atomic.Bool` (initially from
`Options.ReadOnly`). The wrapper checks it before delegating to a mutating method, and the hook
checks it for every request. `ReadOnly(e)` keeps its test-facing meaning (a guard fixed to
read-only), and `IsReadOnly` reads the flag.

Rejected alternative: rebuild the engine on every toggle, as a membership change would. It
would drop the event stream, stats, logs and lists, and on an `ssh://` launch daemon it would
either need the interactive tunnel again or fail on a passphrase. It also creates a window in
which the old read-write engine is still live while the UI already says read-only. A shared
atomic flag has no such window for requests that start after the toggle, keeps both D17 layers,
and costs one branch per request. Mutating requests already in flight are not recalled, which
the spec states.

The reflection tripwire still applies: `ReadOnly` and `SetReadOnly` are read methods (they
change the desk's own state), every mutating method remains overridden, and the count assertion
moves from 25 to 27 interface methods.

### D5 — A member owns an intended mode; the lock is separate

`session.Member` stores `readOnly` as the operator's intended per-member mode.
`Member.ReadOnly()` reports the effective mode: true when the session's `ReadOnlyLock` is set,
otherwise the intended value. `Member.Mode()` is what persistence writes. `--read-only` sets
`Config.ReadOnlyLock`, never rewrites intended modes, so one cautious run does not permanently
make every stored daemon read-only; a member added under the lock is stored read-write, which is
what it would have been without the flag.

`Session.Add(ep, readOnly)` and `AddLaunch(..., readOnly)` take the intended mode. `DialOptions`
keeps its other fields but loses `ReadOnly`; the connect path builds the engine in the effective
mode at that moment, and applies `engine.SetReadOnly` again when the member connects, so a
toggle made while a member was still connecting is not lost.

### D6 — Toggling is a session operation with the session's own guards

`Session.SetReadOnly(id, bool)` refuses when the lock is on (`ErrReadOnlyLocked`) and when the
member has a running job (`ErrJobsRunning`, checked under the job manager's lock as removal
is). Otherwise it flips the member's intended mode, sets the engine's mode, bumps the member's
generation if needed for redraw, and returns. The UI maps the refusals to the same wording style
as removal refusals. Nothing is sent to a daemon by the toggle itself.

### D7 — The UI gates on the member the action will reach

`Model.readOnly` (a cached flag) is replaced by:

- `readOnlyLock bool` — the run's lock;
- `m.readOnly()` — effective mode of the focused member, used by the ordinary action gate,
  help, forms, save/export and dialog wording;
- `m.readOnlyFor(mem)` — effective mode of an explicitly named member.

`invokeOn` keeps refusing `a.mutating` actions against `m.readOnly()`, except the image copy,
whose action is gated against its destination (`copyProblem` uses the target member, and the
command-line/session lock still overrides). The rest of the 76 sites are a mechanical rename to
`m.readOnly()`; the compiler finds them. Keeping a cached field was rejected because a stale
cache in a safety gate is exactly the kind of bug this change cannot afford.

### D8 — The strip, header and picker show a member's mode from the session

`stripLabel` marks a read-only member `ro`; the header adds `[READ-ONLY]` for the focused
member and treats it as never-dropped, next to the link tag. The picker shows `read-only` or
`read-write` in words for each member, explains the lock in its head when `--read-only` is in
force, and binds `o` to the selected member. `o` is free in the picker (its existing keys are
`Enter`, `d`/`x`/`delete`, `r`, the movement keys, `g`/`G`, `Esc`, `q`). The toggle updates the
session, the strip, the header, the signature used for redraw and the save; it opens no
dialog because it sends nothing and every mutation still has its own confirmation.

### D9 — The launch still decides the daemon; the file only supplies members and a pending focus

`main` loads state before resolving the endpoint. The stored theme is used only after the flag
and `DOCKER_DESK_THEME`, and the requested theme name (`auto` when the automatic choice applies)
is passed to the UI so that `auto` stays `auto` rather than being pinned to the resolved
`dark`/`light`.

The launch engine's initial mode is the stored mode for that endpoint unless `--read-only` is
in force; matching uses `session.Canonical` on the resolved endpoint. Every other stored member
is resolved and handed to `ui.New` as a restored member:

1. A non-`default` context name is resolved from the Docker context store; the context is
   authoritative, so its TLS material and endpoint are what the CLI would use.
2. Otherwise a stored `unix://` or `ssh://` endpoint is used directly after the ordinary
   endpoint validation.
3. A stored `tcp://` endpoint with no resolvable context is skipped with a notice: its TLS
   files were deliberately not stored, and guessing plain TCP could silently downgrade a
   connection that used TLS.

After `New` has added the launch member, the UI adds the restored members with `sess.Add(ep,
mode)`. They connect in the background under the existing no-questions rule. The stored focus
key is held as `restoreFocus`; `syncFocus` applies it the first time a member with that key is
connected, and the status line names the daemon. Any explicit focus change by the operator
(picker, `>`/`<`, an Attention jump) clears `restoreFocus`, so a slow daemon cannot yank focus
from under the operator. If it never connects, the desk stays where it is.

Rejected alternative: make the stored focus the launch daemon. It would contradict two settled
behaviours — the launch selection precedence and "verify the launch daemon before taking the
terminal" — and a file would then be able to prevent the desk from opening. The user chose the
non-overriding option.

### D10 — Persistence is a snapshot written synchronously, with failures reported once

`internal/state.Store` exposes `Load`, `Save(File)` and `Enabled`. The UI holds a small
`sessionFile()` builder (theme choice, focused key, members from `Endpoint()`/`Context()`/
`Mode()`) and calls `Save` after: a theme change, adding a member, removing a member, an actual
focus change and a mode toggle. `--no-state` passes no store, so the UI never reads or writes.
Headless paths (`--check`, `--report`) never save.

The file is tiny, so the write happens on the UI goroutine; the alternative — a background
writer — adds ordering and shutdown questions for no measurable gain. A failure is kept in a
`saveNoticeShown` flag and reported once, in the status line; later failures are suppressed
until a later success, so a full disk cannot fill the status line.

### D11 — `--no-state` is the only new launch switch, and theme persistence is a deliberate rule change

`--no-state` disables load and save. `--check` honours it, and prints the effective mode of the
launch daemon after applying the stored mode. `T` now stores the choice; this intentionally
replaces the old "the choice SHALL NOT be stored" rule. The status line says the theme was
saved, and `--no-state` says it is not being kept. Stored `auto` remains automatic.

## Risks / Trade-offs

- **A writable state file is a local input that chooses members and modes** → The file cannot
  change the launch daemon, cannot introduce credentials, and every restored member is subject
  to the same endpoint validation, background connection and no-questions rule as the picker.
  The 0600/0700 modes, owner check and refusal of group/world-writable or symlinked files limit
  who can influence it. `--read-only` overrides every stored mode, and `--no-state` removes the
  input entirely. A malicious file can still add a daemon the operator did not expect to the
  session; the strip and the focus notice make that visible, and mutations still need an
  explicit confirmation naming the endpoint.
- **A stored read-write member may be more powerful than the operator remembers** → The strip
  marks every read-only member, the header shows the focused member's mode, and the picker shows
  each member in words. `--read-only` remains the blunt instrument.
- **Toggling read-only does not recall requests already on the wire** → Stated in the spec and
  in the help; the same limitation exists today between a key press and the daemon receiving a
  request. The wrapper and hook stop everything that starts after the toggle.
- **A write can race another instance of the desk** → Atomic rename means no torn file; the last
  writer wins. State is a convenience, not a lock or a source of truth about running work.
- **The state file can go stale** → Endpoints that no longer resolve are skipped with a notice;
  unreachable ones remain disconnected and retried, exactly like picker-added members.
- **Pinning a theme accidentally** → `auto` is stored as `auto`, and the flag and environment
  still win. The status line says the choice was saved.
- **Storage of endpoint text** → An endpoint can contain a user name and a path, but never a
  password (those are refused at launch) or a private key. The file is 0600. Anyone who can read
  it already knows the operator's Docker contexts.

## Migration Plan

No migration is required. A missing file is the current behaviour. Existing users get an empty
file only when they first change something. Rolling back means deleting the file or running with
`--no-state`; an older binary ignores unknown keys only as far as the version check, so the safe
rollback is deletion, which costs only the remembered desk.

## Open Questions

None. The toggle key (`o`), the file name and the exact notice wording are implementation
details settled by this design.
