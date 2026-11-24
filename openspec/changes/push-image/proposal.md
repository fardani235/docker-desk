# Proposal

## Why

The desk's registry side reads: it lists tags, describes them, compares them with local images,
checks upstream and pulls one build by digest. The one thing it cannot do is the other half of a
registry round — send a build that is on this daemon back to its registry. Today that means
leaving the desk for `docker push`, against whichever daemon is focused, with the credential
handling and consent outside everything the desk has established. A complete round should not
stop at the read.

## What Changes

- Images gains `p` (push), next to `P` (pull): the highlighted row's existing tag is uploaded
  to the registry it names. An untagged image, a selection, and `--no-registry` cannot push;
  the desk never retags, never pushes all tags, and never builds.
- The confirmation is typed with the tag, like a pinned pull. It names the full reference, the
  image's short ID, the registry, the credential source (never its value), what will be
  uploaded, and that the remote tag will be created or moved and cannot be undone from here.
- The reference is re-inspected immediately before the push; if it no longer names the image
  that was shown, nothing is sent. The local image and tag are not changed.
- The credential comes from the operator's Docker configuration, exactly as the CLI resolves
  it (auths, `credsStore`, `credHelpers`); the source is shown, the values never are. It is
  read again when the push starts, and sent to the daemon as `X-Registry-Auth`.
- The push runs as a cancellable job: layer progress, the final digest from the daemon, and a
  clear statement that cancelling may leave already-uploaded layers in the registry while the
  tag may not have moved. Registry refusals are shown in the daemon's words.
- Read-only refuses it at the action and again in the engine, because a push is a `POST`.
  `--no-registry` refuses it too: it is a registry request by definition.

## Capabilities

### New Capabilities

- `image-push`: pushing one highlighted image's existing tag — the action, typed confirmation,
  reference pin, credential source and resolution, progress, cancellation and failure
  reporting.

### Modified Capabilities

- `mutation-safety`: adds publishing to a registry as a typed, deliberate action: the tag is
  typed, the remote effect is named, nothing local changes, and read-only/`--no-registry`
  refuse it before any request.

## Impact

- `internal/engine`: `PushImage` (moby `ImagePush`, progress and result), the read-only refusal,
  and `internal/fakeengine`'s `POST /images/{name}/push` with its request log and stream.
- `internal/plan`: `OpPushImage`, a push plan with a typed tag, a pinned reference, a credential
  callback invoked only at execution, and the job's logging.
- `internal/ui`: the Images `p` action, a preparation step that reads the credential source,
  the use of the existing typed dialog, and progress.
- `internal/registry`/`internal/freshness`: reuse of credential resolution and
  `X-Registry-Auth` encoding; no new credential format.
- `README.md` (Images keys, registry round, “What it does not do”), `docs/DECISIONS.md`,
  `docs/TESTING.md`, and the Images screen captures.
- Tests: engine request/progress/auth, plan pin and execution, UI consent and failures, a fake
  engine push, and an end-to-end pty push.
- No new dependency; the moby client already exposes `ImagePush`.
