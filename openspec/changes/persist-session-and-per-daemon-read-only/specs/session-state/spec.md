# Spec Delta

## Purpose

Defines the small local file in which the desk remembers the theme and the shape of the session
between runs: where it lives, what it may and may not contain, when it is read and written, and
how a corrupt, unreadable or untrusted file is kept from affecting the launch.

## ADDED Requirements

### Requirement: The state file lives in the user configuration directory
The program SHALL keep its state in `docker-desk/state.json` under the directory the operating
system gives for user configuration — `$XDG_CONFIG_HOME` when set, otherwise
`~/.config` on this platform. It SHALL create that program directory with mode `0700` when it
writes, and the file with mode `0600`. `--no-state` SHALL disable both reading and writing.

#### Scenario: Default location
- **WHEN** the desk saves state and `XDG_CONFIG_HOME` is unset
- **THEN** the file is `~/.config/docker-desk/state.json`

#### Scenario: XDG override
- **WHEN** `XDG_CONFIG_HOME` names a directory
- **THEN** the file is `<that directory>/docker-desk/state.json`

#### Scenario: Permissions of what is created
- **WHEN** neither the program directory nor the file exists and the desk writes state
- **THEN** the directory is created with mode `0700` and the file with mode `0600`

#### Scenario: --no-state
- **WHEN** the program is started with `--no-state`
- **THEN** no state file is read, the session starts as if none existed, and no state file is written or replaced for the whole run

### Requirement: The state file holds the desk, never credentials
The state SHALL hold the theme name, the endpoint and context name of every member, the focused
member, and each member's read-only flag. It SHALL NOT hold passwords, tokens, passphrases, key
material, or any other credential or secret. Connection details that are credentials or are
read from the Docker configuration — TLS certificate files, ssh keys, authentication — SHALL be
resolved again at launch and SHALL NOT be copied into the file.

#### Scenario: An SSH member is stored
- **WHEN** a member reached over `ssh://` is saved
- **THEN** its endpoint and context name are stored, and nothing about its key, agent, passphrase or known hosts

#### Scenario: A context member is stored
- **WHEN** a member that came from a Docker context is saved
- **THEN** its context name is stored, and its TLS certificate files are read again from that context on the next launch

#### Scenario: The file is read by a person
- **WHEN** the state file is opened in an editor
- **THEN** it contains no password, token, passphrase or key material

### Requirement: The state file is written as the desk changes
The program SHALL write the state when the theme changes, a member is added or removed, focus
moves, or a member's read-only mode is toggled, so that a later launch reflects the desk as it
was. A write SHALL replace the file atomically and SHALL never leave a partial file behind. It
SHALL be best effort: a failed write SHALL be reported once, SHALL NOT interrupt the desk and
SHALL NOT be retried in a loop.

#### Scenario: Theme changed and the desk quit
- **WHEN** the operator cycles to another theme and quits
- **THEN** the next launch, with no theme flag or environment variable, starts in that theme

#### Scenario: A member removed
- **WHEN** the operator removes a daemon from the session
- **THEN** the next launch does not restore it

#### Scenario: Write fails
- **WHEN** the state cannot be written — the directory is not writable, or the disk is full
- **THEN** the desk says so once, keeps working, and sends nothing to any daemon because of it

#### Scenario: Forced read-only is not persisted
- **WHEN** the desk runs with `--read-only` and the operator changes members, focus or theme
- **THEN** those changes are saved, but no member's stored read-only flag is overwritten by the forced mode

### Requirement: A bad state file never stops the launch
A state file that cannot be read, is not a regular file owned by the operator, is group- or
world-writable, does not parse, has an unknown version, or has an entry of the wrong type SHALL
be treated as absent, and the desk SHALL open as if there were no state. The operator SHALL be
told once, in words that name the file and the problem; nothing SHALL be written over the file
before the desk has opened. The file SHALL NOT be silently repaired.

#### Scenario: Truncated JSON
- **WHEN** the state file contains invalid JSON
- **THEN** the launch proceeds with no restored state and the operator is told once that the file was ignored

#### Scenario: Unknown version
- **WHEN** the state file declares a version this program does not know
- **THEN** it is ignored as a whole, with one notice, and is not rewritten from defaults during that run

#### Scenario: Unreadable file
- **WHEN** the state file exists but the operator may not read it
- **THEN** the launch proceeds and the notice names the file and the permission problem

#### Scenario: Unsafe permissions
- **WHEN** the state file is a regular file owned by the operator but is writable by group or others
- **THEN** it is not trusted, the launch proceeds without it, and the notice says why

#### Scenario: One bad entry
- **WHEN** the file parses but one member entry is malformed
- **THEN** no part of the file is applied and the operator is told once that the file was ignored

### Requirement: Restored state cannot widen what the launch may do
State SHALL be an input to the session, not an authority over it. A stored member SHALL pass the
same endpoint validation as any other and SHALL be added as a background member that never asks
anything on the terminal. The stored focus SHALL be applied only after that member has
connected. State SHALL NOT override the launch daemon chosen by the existing precedence, SHALL
NOT make a member read-write while `--read-only` is in force, and SHALL NOT cause a request
before the workspace opens.

#### Scenario: Stored member is no longer valid
- **WHEN** a stored endpoint is now unsupported or its context no longer exists
- **THEN** that member is skipped with a notice and the rest of the launch is unaffected

#### Scenario: Stored focus is unavailable
- **WHEN** the stored focused member cannot be reached at launch
- **THEN** the desk opens on the launch daemon, keeps trying the stored member in the background, and does not wait on it

#### Scenario: --read-only with stored read-write members
- **WHEN** the state says a member is read-write and the program is started with `--read-only`
- **THEN** that member is read-only for the run

#### Scenario: Stored ssh member needs an answer
- **WHEN** a restored `ssh://` member would need a passphrase, a password or a host-key confirmation
- **THEN** nothing is asked on the terminal, the member is shown disconnected with the reason, and the desk opens
