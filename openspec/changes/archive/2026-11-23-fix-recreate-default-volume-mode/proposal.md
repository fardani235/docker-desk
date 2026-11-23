# Proposal

## Why

The re-create action refuses every container whose named volume was mounted without an explicit
mode. On Linux the daemon records `Mode: "z"` for such a volume, whatever its driver default is
(confirmed on this machine's Docker 29.10.0, API 1.57, for both `-v name:/path` and
`--mount type=volume`). The clone reads any `z`/`Z` as an SELinux relabel it cannot reproduce, so
it withdraws `A` and names "mount option z" as a setting the desk does not carry. The setting is
the daemon's own default, the volume is carried by name, and the daemon re-applies the default on
the new container: the refusal is false, and it makes the feature unusable for most stateful
containers, including the ones the desk itself creates.

## What Changes

- A `z` mode on a **volume** mount stops being a refusal. The mount is carried by volume name as
  before, and the daemon re-derives its default mode when the new container is created.
- `Z` on a volume, and `z` or `Z` on a bind, stay refusals: a private label belongs to the
  container it was made for, and a bind's relabel cannot be expressed by the desk's mount type.
  Every other mode token stays a refusal.
- The fake reports the daemon's default `Mode: "z"` for a named volume on the default driver, so
  the re-create path is exercised against the daemon's real shape; unit tests cover the default
  volume, `Z` on a volume, and `z` on a bind.
- The CLI cross-check compares the mount's `Mode`, and the read-only real-daemon check reports a
  skipped storage comparison instead of passing silently. Documentation states the exception.

## Capabilities

### New Capabilities

None. The behaviour belongs to the capability the re-create round introduced.

### Modified Capabilities

- `container-recreation`: storage is carried for a named volume in the daemon's default mode, and
  the refusal rule names the relabel cases that remain refusals rather than "any mode other than
  read-only".

## Impact

- `internal/plan`: `mountModeExtras` and `cloneStorage` gain the volume/`z` exception, with tests
  for the default volume, `Z` on a volume, `z` on a bind and combined modes such as `ro,z`.
- `internal/fakeengine`: `mountPoints` reports the default mode for a volume on the local driver,
  and its create path records an explicit mode; the re-create and clone tests use it.
- `scripts/cli-crosscheck.sh`: the clone case compares `{{.Mode}}` for a default named volume,
  a read-write bind and a read-only bind.
- `internal/plan/recreate_real_test.go`: a refused storage family is reported, not silently
  skipped.
- Docs: the README mount paragraph, `docs/DECISIONS.md` (round nineteen, G18) and
  `docs/TESTING.md` state which relabel modes are refused and which are the daemon's default.
- No engine API change, no new request, and no mutation against a real daemon.
