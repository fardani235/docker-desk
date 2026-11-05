## Why

The desk shows what the daemon says about an image — its tags, size, config — but not what the
image *is*: the files each layer adds and deletes, the secrets a later layer "removed" that
every copy of the image still carries, the setuid bits, the binaries and how they were built,
the attestations that travel with it. And it shows none of that next to what the daemon is
running from the image now. The bytes are one GET away (`docker save`'s stream), and reading
them changes nothing.

## What Changes

- **A third Images tab, Findings.** Opening it analyses the highlighted image from its own
  archive, as a cancellable job with progress in bytes read and layers found, and keeps the
  report by image ID and platform; a second open is instant. On a remote daemon an image over
  500 MB waits for Enter.
- **`engine.SaveImage`**, a read method: the client library's `ImageSave` for one image ID and
  its platform (`GET /images/get?names=…&platform=…`). Read-only mode passes it through.
- **`internal/forensics`** streams the archive once — nothing on disk, every buffer bounded —
  reading `manifest.json` for the config and ordered layers and `index.json` for what it omits
  (attestations); absent blobs of a nested index are skipped. Each layer tar is walked for
  added files, whiteouts, opaque directories, size, file count, modes, setuid/setgid, xattrs
  and bounded content.
- **Findings** carry rule, source (daemon, image, desk), certainty (certain, likely, hint),
  layer, path, masked evidence, a one-line reason, the build instruction (only when the
  history's layer-making entries map one to one) and what cannot be known. No severity score.
  Rules: config, layer, filesystem, secrets, binaries, supply; plus live findings joined from
  the daemon's current state, recomputed every frame.
- **Secrets** are masked; `v` reveals the one under the cursor until it moves. A secret's value
  lives in a type that prints only a mask, so it cannot reach a job, an error, a file or a log.
- **`o`** cycles the order: layer, group, certainty.
- **The fake daemon** serves `GET /images/get` and `/images/{name}/get` from archives built by
  `internal/forensics/fixture`, in the classic and the OCI layout; `shop/api:dev` in the demo
  carries a deleted credential and the rest.

## Impact

- `internal/engine`: `SaveImage`; `Container.NetworkMode`; `ImageDetail.Variant`; `Platform`.
- New `internal/forensics`, `internal/forensics/fixture`.
- `internal/ui`: the Findings tab, its keys, hints and help.
- `internal/fakeengine`: image export, privileged / network mode / user on containers.
- No new module; `opencontainers/image-spec` becomes a direct requirement (it was indirect).
