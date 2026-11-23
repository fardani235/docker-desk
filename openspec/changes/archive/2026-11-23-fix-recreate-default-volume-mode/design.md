# Design

## Context

See `proposal.md` — Why. The constraints that shape this work:

- `plan.Clone` parses the container's raw inspect into a narrow model. `rawMountPt` already
  carries `Mode`, and `mountModeExtras` (`internal/plan/recreate.go:820`) treats every token
  other than `ro`, `rw`, `rprivate` and the empty one as a setting the desk cannot reproduce.
- The daemon writes the mode it mounted with into the container's record: `setBindModeIfNull`
  (`daemon/volumes_unix.go:128`) sets `Mode: "z"` for a named volume on the default driver when
  the mount carried no mode, and `GetMountPoints` copies it into inspect. This machine's Docker
  29.10.0 (API 1.57) confirms it for `--mount type=volume` and `-v vol:/path`; `:rw` yields
  `"rw"`, `:ro` yields `"ro"`, and an anonymous volume yields `""`.
- `engine.MountOption` has no mode and `createConfig` (`internal/engine/moby.go:914`) sends
  `mount.Mount{Type, Source, Target, ReadOnly}`; the daemon derives the mode again at create,
  so a carried volume needs no mode.
- The fake's `MountSpec` (`internal/fakeengine/containers.go:32`) and `mountPoints()` (`:433`)
  never report a mode, and its create handler (`internal/fakeengine/create.go:248-280`) records
  no mode, so every fake container looks kinder than the daemon.
- The unit fixture (`internal/plan/recreate_test.go:252`) used `Mode: "rw"` for the named
  volume, `scripts/cli-crosscheck.sh:252` prints no `Mode`, and the real-daemon check
  (`internal/plan/recreate_real_test.go:192`) skips every mount comparison once `FamStorage` is
  refused. The defect sat outside every proof.

## Goals / Non-Goals

**Goals**

- A named volume in the daemon's default mode is carried, and the action is offered.
- The relabel cases that carry operator intent — `Z` on any mount, `z` on a bind — still refuse,
  and the refusal still names the option and the destination.
- The fake reports the daemon's default mode, so the re-create path is exercised against the
  real shape; the cross-check compares the mode the daemon records.

**Non-Goals**

- Carrying a mount mode as an option. The desk's mount type has no options, and the engine is
  not widened.
- Classifying every historical mode string; only the daemon's default is special-cased.
- Changing which mount types are carried, or how a volume's name is resolved.

## Decisions

### 1 — `z` is the daemon's default on a volume; `Z` and bind labels remain refusals

`mountModeExtras` gains the mount type. For a volume, the token `z` is accepted and every other
token keeps its current verdict; for a bind, `z` and `Z` both stay refusals. `Z` stays a refusal
on a volume as well.

*Why per type:* the daemon sets `z` only for volumes (`setBindModeIfNull` is called in the two
named-volume branches). On a bind the same token was chosen by the operator (or the tool that
built the run command) and a relabel cannot be expressed by `MountOption`, so refusing is
honest. `Z` marks a private label tied to the old container's MCS label; the desk cannot express
it either.

*Alternatives:* ignore `Mode` for volumes entirely — simpler, but silently accepts a `Z` and
loses a refusal the spec keeps; read `HostConfig.Binds` to decide whether the mode was explicit —
the default `z` and an explicit `z` are indistinguishable there, and a container created through
the API's `Mounts` has no mode field at all; treat every `z` as the default — wrong for binds.

### 2 — No mode is carried; no engine change

The clone keeps producing a `MountSpec` with type, source, destination and read-only. The create
sends no mode, and the daemon applies its default again on the new container. `ResourceLimits`
and `ContainerCreateOptions` are untouched.

*Why:* the only faithful representation of "the daemon's default" is to leave the field unset.
Widening `MountOption` to carry a mode would add a field whose only legal value is the default,
and would still be unable to express `Z`.

*Alternatives:* add `Mode` to `engine.MountOption` and thread it through `createConfig` — a wider
API with no behaviour to express; retag or relabel the volume as a separate step — a second
mutation outside the plan, and the plan deletes nothing and creates no side effects.

### 3 — The fake reproduces the daemon's mode

`fakeengine.MountSpec` gains `Mode`. The create handler records the mode a `-v` spelling carries
(`parts[2]`) and leaves it empty for a typed `Mounts` entry; `mountPoints()` reports `z` for a
volume whose mode is empty and whose driver is the fake's only (local) driver, and reports the
recorded mode otherwise. Seeds keep an empty mode and therefore report `z` for their named
volumes, like the daemon.

*Why:* the defect survived because the fake was kinder than the daemon. A fake that reports the
real default makes the clone test, the re-create test and the end-to-end path assert the shape
the operator will meet.

*Alternatives:* leave the fake alone and prove the rule only from raw inspect fixtures — cheaper,
but the re-create path and the dialog stay blind; make the fake always report the daemon default
for binds too — wrong, and it would hide a legitimate refusal.

### 4 — The proofs are extended in three places

- The clone's tables: a named volume with `Mode: "z"` is carried (the fixture at
  `recreate_test.go:252` changes from `"rw"` to `"z"`); `Z` on a volume, `z` on a bind and a
  combined `ro,z` on a volume each get a case in the refusal table.
- `scripts/cli-crosscheck.sh`: the mount formats for the create and re-create cases gain
  `{{.Mode}}`, with the daemon's answers recorded (`--mount` volume `z`, `--mount` bind empty,
  `-v …:ro` `ro`). This documents the wire shape the clone reads.
- `recreate_real_test.go`: a refused storage family is logged with the refusal text instead of
  being skipped silently. A per-mount comparison is not used, because a mount refused for a
  different reason (propagation, subpath, a driver) would be reported as dropped.

### 5 — Documentation states the exception

The README's mount paragraph, `docs/DECISIONS.md` (a round twenty) and `docs/TESTING.md` gain the
rule and its evidence: `z` on a volume is the daemon's default and is not refused; `Z` and a bind
label are.

## Risks / Trade-offs

- **A future daemon starts defaulting binds to `z`.** The false refusal would return for binds.
  → The rule keys on the mount type, and the real-daemon check now logs the skipped storage
  family instead of passing silently; a new default would be visible there.
- **An explicit `z` on a volume becomes indistinguishable from the default.** → Accepted: the
  daemon applies the same label either way on the new container, so the behaviour is identical.
- **`Z` on a volume stays refused even though the daemon would re-apply a private label.** →
  Conservative by design; the operator can re-create by hand. Recorded in DECISIONS.
- **The fake's default is tied to its single local driver.** → The fake models no other driver;
  the real-daemon check and the cross-check are the evidence for anything else.

## Migration Plan

- No stored state, no schema, no configuration, no engine API change. The create request is
  unchanged; the fixed path sends the same request it sent before for every other container.
- Rollout is the binary itself. Rollback reverts the change; no data is created by it.
- After the code and tests, archive the change and verify the main spec, DECISIONS and TESTING
  agree.

## Open Questions

None. The remaining choice — whether a private label on a volume should ever be carried — would
change the spec and is deliberately left out of this round.
