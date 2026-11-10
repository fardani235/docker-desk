# Design

## Context

See `proposal.md` for why. The constraints that shape the approach:

- The only project-level knowledge today is labels: `plan.Projects` reads
  `com.docker.compose.project`, `.service`, `.project.working_dir` and `.project.config_files`
  (`internal/plan/compose.go`). Nothing opens a file, and the Details pane says so in a
  sentence (`internal/ui/details.go:564`).
- The desk reads files today only at paths it chose: `$DOCKER_CONFIG`/`~/.docker`, TLS
  certificates, Docker contexts. `compose-projects` also already carries the `config_files` and
  `working_dir` labels live containers record, plus one the desk does not yet name:
  `com.docker.compose.config-hash`.
- The house style for a format the standard library lacks is a narrow, bounded reader with its
  refusals written down (the read-only SQLite btree walker, the streaming SBOM scanner). Those
  exist because a request forbade dependencies (`docs/DECISIONS.md:1841`), not because
  hand-rolling is a value in itself.
- The interaction model for an expensive, on-demand comparison already exists: Changes shows
  what is cheap, states that nothing is read before `Enter`, and runs the read as a job.

A live example on this machine (`/home/ridwan/workspace/gitlab/compose.yaml`) shows the real
shape of the declared side: `image: gitlab/gitlab-ce:${GITLAB_VERSION:-19.5.1-ce.0}`, a
`.env` beside it, a 20-line block scalar containing a flow map, and a container created four
seconds after the file was written.

## Goals / Non-Goals

**Goals:**
- Turn the project's existing claim ("these containers came from that file") into something
  that can be checked, using the four cheapest true comparisons.
- Keep every refusal explicit and inspectable; no row may be filled by a guess.
- Leave the desk's reading surface one flag and one file away from being switched off.

**Non-Goals:**
- Any compose lifecycle: no `up`, `down`, `build`, `pull`, `scale`, no binary, no writer.
- Reconstructing what the file said when the containers were created. That record does not
  exist.
- Full configuration comparison (ports, volumes, environment, command, health checks).
- Reading a file on another machine.

## Decisions

### Y1 — Compare the service set and a literal image, nothing else

**Decided:** rows are exactly: declared service without a container, container whose service
the file does not declare, and an image that differs when both sides are literals. Plus the
ticket facts in the header (paths, which machine, file write time, per-container config hash).

**Alternatives:** fingerprints only (path, existence, mtime, config hash — no parser). Rejected
as a feature that reports a timestamp and no verdict; it is the header of this screen, not a
screen. Full config comparison. Rejected because merge order, interpolation and normalisation
make most rows end in "not comparable", which is not a comparison but a wall of caveats.

**Why:** the two set rows catch the mistakes that matter (a service the operator believes is
running; a container the file no longer describes), and they are correct without interpolation.

### Y2 — `go.yaml.in/yaml/v3`, decoded into a flat struct

**Decided:** one module, `go.yaml.in/yaml/v3` (v3.0.6). Decode into
`struct { Name string; Services map[string]struct{ Image string; Profiles []string } }` with
typed `string` fields. Any decode error, any document with a `!` tag, any multi-document file:
refuse the whole comparison.

**Alternatives:**
- `gopkg.in/yaml.v3` — same API, frozen at v3.0.1 (2022), and the module the new path replaces.
- A documented subset scanner — matches the SQLite and SBOM precedent, but the precedent was
  forced by a request. YAML's scanner has to skip block scalars, quoted strings containing
  colons, flow collections and anchors to read the fields at all, and every one of those is a
  place to be quietly wrong. The live file above is exactly that case.
- `docker compose config --format json` — runs the tool D15 forbids, needs a binary the desk
  does not control, and interpolates with today's environment.
- No parser at all. Excluded by Y1's scope.

**Why:** the decision is not "dependency vs none" but "how does this fail". A full parser
fails loudly; a scanner fails plausibly. Anchors and `x-` extension fields are common in real
files, and a strict scanner would mark those projects "not comparable" — useless where it
matters. This is the first runtime dependency added for a file format, recorded as a
round-fourteen decision that supersedes D15, and the `recover` net around `Parse` follows K2:
a test calls it without the net so a fault is found rather than caught.

### Y3 — One read, on request, and never a rendered byte

**Decided:** nothing touches the filesystem until `Enter` on the tab. The read is a job: open
one regular file, read at most 1 MiB, `os.Stat` first to refuse directories, devices, FIFOs and
oversized files. The file's bytes are parsed and discarded; the tab shows only the comparison
and, on failure, one sentence. No preview, no "first lines".

**Alternatives:** stat on highlight to show existence and age (nicer header, but touches an
attacker-influenced path on every arrow key); render a short preview of an unparseable file
(more useful, and the one place a hostile label could disclose a file).

**Why:** the path comes from container labels, and image labels propagate into container
labels, so an image can manufacture a project pointing at `/etc/shadow`. Never rendering the
bytes makes that read disclose nothing. It also keeps three standing promises literally true:
a list refresh asks nothing, searching sends nothing, nothing is read before Enter.

### Y4 — Local transport only

**Decided:** compare only when `dockerctx.Target.Transport` is `TransportUnix`. Over SSH or TCP
the tab says the path belongs to the machine that ran compose and reads nothing.

**Alternatives:** read the local path anyway and add a caveat. Rejected: at the same path on
this machine there may be a different file, and a wrong answer is worse than none. Reading the
remote file over the existing SSH connection. Rejected: a new class of remote execution for a
read the desk can decline.

**Why:** a Unix socket is a same-machine promise the desk already relies on for credentials and
certificates. Docker Desktop on macOS and Linux forwards a Unix socket from a filesystem the
desk can read, so the gate does not exclude the common desktop case.

### Y5 — No interpolation; `${` is a refusal, not a value

**Decided:** an image value containing `${` is reported as a template and not compared. The
desk does not read `.env`, does not apply defaults, does not compare the template's default
against the running value.

**Alternatives:** read the `.env` beside the file and interpolate (incomplete: compose's
precedence is shell environment first, and that shell is gone); interpolate defaults only
(tempting, since the live example's default happens to match, but it is a guess presented as a
fact).

**Why:** the declared side is partially evaluated source, not data. This is the single
behaviour that keeps the feature from manufacturing confident wrong answers.

### Y6 — Profiles and one-offs are not drift

**Decided:** a declared service with no container that declares `profiles` is reported as
"declared with profiles; the desk cannot know if they were enabled" and is not counted as
missing. Containers with `com.docker.compose.oneoff=True` are excluded from the actual side.

**Why:** no label records which profiles were active, and `docker compose run` leaves containers
that would otherwise read as a service with too many instances. Both would produce false alarms
on the most common real setups.

### Y7 — Time is a fact in the header

**Decided:** the file's write time is reported against the newest container's creation time as
"written 4s before / 3h after these were created". No "modified since deploy" verdict.

**Why:** a `git checkout`, a `touch`, a bind mount or clock skew all move mtime. Upstream
already refuses to call a build newer on the strength of a producer's timestamp; the same rule
applies here.

### Y8 — Package boundaries

```
internal/composefile      Parse(bytes) -> File        stdlib + yaml only
                          Read(path) -> bytes         the bounded reader
                          Compare(File, []Actual) -> Result
                                                      no daemon, no UI, no engine types

internal/plan             containers -> []composefile.Actual   (it already has the snapshot)

internal/ui               tab, header, job wiring; renders Result
```

`Compare` takes plain structs, like `internal/report` does, so a change in the engine model
cannot reach into the comparison and the comparison cannot reach the daemon. `File` keeps only
`name`, `services[name]{image, profiles}`; nothing else is retained from the document.

### Y9 — `--no-compose-files`, and sandbox fixtures that exist

**Decided:** a flag with `--no-registry`'s shape forbids the read; the refusal names the flag.
`fake-dockerd` gains a compose-directory flag, and `make sandbox` writes fixture files into a
temporary directory, adjusting the seeded labels, including one project whose file is
deliberately absent so the "cannot be compared" path is demonstrated. The fixture set covers a
template image, a profiled service, a missing service, an undeclared container and a moved tag.

**Why:** today the seeded labels point at `/home/dev/src/shop/compose.yaml`, which does not
exist; without fixtures the sandbox would show the feature entirely as a refusal.

## Risks / Trade-offs

- **The promise moves in five places at once** (spec, README twice, DECISIONS, the UI sentence)
  → the doc edits are tasks in the same change as the code, not a follow-up.
- **A hostile label points the desk at a readable file** → no byte is ever rendered, the read is
  capped at 1 MiB of a regular file, the operator asks for it, and `--no-compose-files` removes
  the class.
- **yaml.v3 on crafted input** (alias expansion, deep nesting) → the 1 MiB cap bounds the
  document; `recover` is the net, and a test calls `Parse` without it.
- **Coverage looks thin on templated projects** → the refusal is a row reason, not a hole, and
  the header counts how many rows were compared. A project with every image templated still
  gets the service-set comparison.
- **Multi-file merge order** → union of service names, last declaration wins for an image,
  `!reset`/`!override` cause a refusal of that service rather than an incorrect row.
- **mtime false positives** → stated as a fact, with both times, so a git checkout reads as what
  it is.

## Migration Plan

No stored state and no data migration. The flag defaults to on; a user who wants the old
guarantee uses `--no-compose-files`. Rollback is deleting the tab; the parser and comparison are
compiled out cleanly because nothing else imports `internal/composefile`.

## Open Questions

None remaining. Two presentational choices were settled while writing the task list, because the
tasks have to name them: the Declared tab is reached with `]` and by a new "compare with the
compose file" entry in the Compose action menu, with no new key (the Compose kind has only `t`
and `d` today and the desk's key space is already crowded); rows are ordered declared only,
differs, present only, not compared, match, then by service name — the same "disagreements
first" default Packages uses.
