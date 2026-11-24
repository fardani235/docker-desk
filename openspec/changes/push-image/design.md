# Design

## Context

The registry read side is complete: `internal/registry` resolves references and credentials,
`internal/repository` lists and describes tags, `internal/freshness` checks upstream, and the
desk pulls a pinned build by digest. `engine.Engine` has no push; `mobyEngine.PullImage` shows
how the daemon's JSON progress stream is aggregated.

The plan/consent machinery already supports a typed word (the pinned pull types its tag), a
step pin (`checkImagePin` re-inspects a reference immediately before acting), and a job that
executes exactly the plan shown. Credentials are read from the operator's Docker configuration
by `registry.ResolveCredential` and encoded for the daemon by `freshness.EncodeCredential`; the
repository browser already displays only the credential's source.

See `proposal.md` for motivation and the two spec deltas for required behavior.

## Goals / Non-Goals

**Goals:**

- Complete the registry round from Images: push the tag that is on screen, to the registry it
  names, with the same consent standard as every other outward action.
- Never expose a credential value, and never send one to a daemon over a link worse than the
  Docker CLI would without saying so.
- Keep the local image and tag untouched, and report exactly what the daemon says.

**Non-Goals:**

- Retagging, pushing a tag the image does not carry, pushing all tags, or `--all-tags`.
- Building, importing or loading images.
- A Registry-browser push surface; this change is the Images action only.
- Choosing a platform explicitly; the daemon pushes what the tag names.
- Changing pull's behavior under `--no-registry`.

## Decisions

### D1 — `p` is the Images action, and only an existing tag can be pushed

`KindImages` gains `action{key: "p", label: "push…", desc: "upload this tag to its registry;
the remote tag is created or moved", needsRow: true, mutating: true}`. `P` is pull, `p` is
push; the key is context-specific like `s`/`p` across kinds. `mutating` makes read-only refuse
it in `invokeOn`, and `needsRow` without `setCapable` refuses a selection one-at-a-time.

An untagged row has no destination. `preparePush` returns `plan.NotApplicable` ("this image has
no tag to push; nothing was sent") rather than opening a dialog with nothing actionable. No
retag is offered: tagging is a separate local mutation and would turn a push into two effects.

**Alternative rejected.** A Repository-browser push would need a target tag and therefore
retagging; an all-tags push publishes to several registries at once, which no single
confirmation can name honestly.

### D2 — A push is a plan with the tag as its typed word and the image ID as its pin

`plan.Push(snap, imageID, ref)` builds:

- `Target{Kind: "image", Name: ref, ID: shortID}`, `Title: "Push image"`, `JobTitle: "push
  image"`, `JobTarget: ref`;
- one `Step{Op: OpPushImage, Kind: "image", ID: ref, Name: ref, Pin: imageID}`;
- `Tier: TierTyped`, `Word: tag` (the tag being published, like `PullPinned`);
- `CredSource` for display and a `RegistryAuth` callback (D3);
- effects: the layers this daemon holds for the reference are uploaded to the registry; the
  remote tag will be created or moved; the local image and tag are unchanged; nothing is
  built; the confirmation should say this cannot be undone from the desk.

`Execute` gains `OpPushImage`: `checkImagePin` runs first (it already inspects the reference and
compares the image ID), then the credential callback, then `eng.PushImage`. A moved tag or a
credential-helper failure aborts before the request and logs why. `describe` prints "push image
<ref>". The progress callback updates `run.Status` and `Fraction`; the final daemon digest is
logged as the daemon stated it, never computed.

**Alternative rejected.** Pushing outside `plan.Execute` would create a second mutation path
and bypass the typed confirmation, arming and job ledger.

### D3 — The credential is a source string in the plan and a callback at execution

`Plan` gains:

```go
// CredSource names where the credential came from, e.g. "auths" or
// "credHelpers ecr-login"; "" means none was found. Never a value.
CredSource string
// RegistryAuth resolves and encodes the credential when the push starts.
// It is never displayed and its result is never logged.
RegistryAuth func(context.Context) (string, error)
```

The plan builder does not touch the configuration. `preparePush` resolves the credential once
to obtain the source (and to surface a helper failure as a blocker), discards the secret, and
sets `RegistryAuth` to a closure that calls `registry.ResolveCredential` and
`freshness.EncodeCredential` for the target reference when the job starts. Resolving again means
a `docker login` between opening and confirming is honored, and no credential lives in the
dialog or the plan text.

`mobyEngine.PushImage` takes the encoded string and passes it as `X-Registry-Auth`; it never
places it in an error. The repository browser's `CredentialSource` display is reused for the
dialog's source line.

**Alternative rejected.** Storing the encoded credential in the plan would put a secret in
memory for the dialog's lifetime and risk it entering a log or a copied plan; resolving it in
the engine would require the engine to know the Docker configuration directory.

### D4 — The engine streams the daemon's push messages

```go
PushImage(ctx, ref, registryAuth string, progress func(PushProgress)) (PushResult, error)
```

`ImagePushOptions{All: false}` only: one tag, no platform, no force. The JSON messages are
aggregated like `PullImage`'s, with `Pushing`/`Pushed`/`Layer already exists` statuses; the
result carries the manifest digest parsed from the daemon's final status when present, and
otherwise empty. Cancel closes the response; the error is the context's. `readOnlyEngine`
returns `ErrReadOnly` before anything is sent. `--no-registry` is checked in the action before
preparing, because the flag's rule is that the desk makes no registry request.

### D5 — The confirmation names the publish and the link

The existing typed dialog shows the reference, short ID, daemon, credential source, and the
effects. It also adds a warning when the focused daemon is reached over a `tcp://` link without
TLS: the `X-Registry-Auth` credential is sent to that daemon over the connection, and the desk
must not imply otherwise. The word is the tag; `y`, Enter without the word, and every other key
send nothing, and the existing arming delay applies. There is no second dialog.

### D6 — Cancellation and failure say what is and is not known

The push job's log ends with the daemon's final digest on success, or with:

- "cancelled: the stream was closed; layers already uploaded may remain in the registry, and
  the tag may not have moved" on cancel;
- the daemon's message on refusal, followed by "the local image and tag are unchanged".

The desk never claims a tag moved on a cancelled or failed push; only the daemon's success
status does.

### D7 — The fake daemon streams a push, and the CLI cross-check validates it

`fakeengine` gains `POST /images/{name}/push`: resolve the image and the `tag` query, record the
request and `X-Registry-Auth`, and stream progress JSON (with `PushDelay` for cancellation
tests) ending in a digest status. Missing images and missing tags answer the daemon's error
shape. `scripts/cli-crosscheck.sh` adds a `docker push` against the fake, so the endpoint and
report are Docker's own client's, not the desk's guess.

## Risks / Trade-offs

- **A push is externally visible and cannot be undone** → The tag is typed, the remote effect
  is named, no force or retag is offered, and only the daemon's success status is claimed.
- **Credentials travel to a remote daemon** → The source is shown, values never are; an
  unencrypted `tcp://` link adds an explicit warning. This is the same path the Docker CLI
  uses, not a new exposure.
- **Cancelled pushes leave blobs** → The job says so and does not claim the tag moved; blobs
  without a manifest are unusable but still consume registry storage.
- **The registry refuses (auth, namespace, quota)** → The daemon's message is shown; the local
  image and tag are untouched.
- **A tag moves between the pin and the upload** → The pin inspects immediately before the
  request; the residual window is the same one removals accept, and the job names both the
  reference and the ID.
- **`--no-registry` and pull** → This change refuses push under the flag as its literal rule
  says; pull is unchanged and outside this change.

## Migration Plan

None. The action is additive; read-only and `--no-registry` refuse it. Reverting removes the
key, the plan op, the engine method and the fake endpoint. No stored data or registry format
changes.
