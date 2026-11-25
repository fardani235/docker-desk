# Design

## Context

The image list rows carry the image ID and one reference (`row.imageRef`); an untagged image
has an ID and no reference. Removal already plans an exact reference: `plan.ImageRemove` sets
`KeepsData` when other references remain, and `checkImagePin` re-inspects it before the
request. The engine has no tag request. The Docker API's tag is `POST /images/{name}/tag` with
`repo` and `tag`; the moby client's `ImageTag` takes a source and a target reference.

See `proposal.md` for motivation and the `image-operations` delta for behavior.

## Goals / Non-Goals

**Goals:**

- Add a local tag and remove one local reference without leaving the desk.
- Make the tag-move consequence visible and re-checked, and make untag incapable of deleting
  the image.
- Keep the source image exact and never contact a registry.

**Non-Goals:**

- Retagging by rewriting several references, tag-all, or registry-side tag deletion.
- Untagging the last reference (that is `d remove`).
- Tagging a selection or several images at once.
- Any registry request, authentication or push; `--no-registry` is unaffected.

## Decisions

### D1 — `t` tags through a form; `u` untags through a typed plan

`KindImages` gains:

- `{key: "t", label: "tag…", desc: "give this image another local reference", needsRow: true,
  mutating: true, run: (*Model).openTagForm}`;
- `{key: "u", label: "untag…", desc: "remove this reference, keeping the image", needsRow:
  true, mutating: true, run: (*Model).prepareUntag}`.

Both are one image at a time (`needsRow`, no `setCapable`) and read-only refuses them through
`invokeOn`. `t` and `u` are free on Images and on its Details tabs; on containers they keep
their pause/unpause meanings because actions are per-kind.

Tagging needs typed input (the new reference), so the form is its confirmation, as the pull
and volume forms are. Untagging deletes a name, so it uses a plan and the typed tag, like the
pinned pull and push.

### D2 — The tag target is normalised and the source is the image ID

`plan.NormalizeTag(input)`:

1. rejects an empty string, spaces and an unparsable reference;
2. rejects a digest (`HasDigest`);
3. applies `TagNameOnly` so no tag means `:latest`;
4. returns the familiar `name:tag` (what the list and the daemon show).

`openTagForm` captures the highlighted row's image ID, its name and refs and opens
`formTag` with one target field and a source line. The source of the request is the **image
ID**, never the reference, so a tag that moves elsewhere before the request cannot redirect
the new tag to another image.

### D3 — A move is shown, and re-checked at the request

The form looks the normalised target up in the current image list:

- absent → "creates this tag";
- this image → "already names this image", and Enter sends nothing;
- another image → names that image and says the tag will move to this one.

The job re-inspects the target immediately before tagging. If it now exists and names a
different image from the one the form showed (including "did not exist" → "another image"),
the job refuses and says the target changed. If it now names the same image, it reports that
nothing needed doing. This closes the window between the form and the POST; the source needs
no pin because it is an ID.

The tag job is a normal background job. The model marks the Images list for refresh when it
starts; the daemon's `tag` event and the periodic poll also pick it up.

### D4 — Untag reuses the removal plan with `KeepsData`, refusing the last reference

`plan.ImageUntag(snap, imageID, ref)`:

- refuses if the image or reference is absent;
- refuses with `NotApplicable` when `len(refs) == 1` — "it is the image's only tag; removing it
  would delete the image — use `d remove`";
- otherwise builds the same step as `ImageRemove`: `OpRemoveImage`, the reference, `Pin:
  imageID`, `KeepsData: true`, size 0; title "Untag image", typed word = the reference's tag,
  effects naming the remaining references and that the data stays.

`Execute`'s existing `checkImagePin` refuses if the reference moved or has become the image's
last one (the `KeepsData` guard), so a race cannot turn untag into a deletion.

### D5 — One local engine mutation, and the fake serves it

```go
// TagImage gives the image source (an ID or reference) the target tag
// locally (POST /images/{name}/tag). No registry is contacted.
TagImage(ctx context.Context, source, target string) error
```

The moby implementation is `ImageTag` with `Source` and `Target`; the read-only wrapper
returns `ErrReadOnly`. `fakeengine` gains `POST /images/{name}/tag?repo=…&tag=…`: it resolves
the source, normalises the target, removes that target from any other image, appends it to the
source, records the request, and emits the image `tag` event. A missing source answers the
daemon's 404 shape.

### D6 — Both work under `--no-registry`

Neither action contacts a registry: tag is a local reference, untag is the local removal
request. `--no-registry` therefore leaves both available (unlike push), and the forms say the
tag is local. Read-only remains the only refusal.

## Risks / Trade-offs

- **Moving another image's tag** → The form names the image and says it will move; the job
  re-checks and refuses if the target changed after the form was read.
- **The source image vanishes** → The request is by ID; the daemon's 404 is the job's error,
  and nothing else is tagged.
- **Untag becoming a delete** → Refused up front on the last reference, and `KeepsData` has
  Execute refuse if it became the last one in the meantime.
- **A tag target differs between form and job** → The inspect immediately before the request
  refuses a target that now names an image nobody was shown.
- **The list does not show the new tag at once** → The images list is marked for refresh and
  the daemon's `tag` event also marks it; the next frame or poll shows it.
- **`--no-registry` expectations** → Tag/untag never touch a registry and remain offered; this
  change does not alter pull or push.

## Migration Plan

None. The actions are additive and local; reverting removes the keys, the form, the untag
planner, the engine method and the fake endpoint. Existing remove, prune and archive behavior
is unchanged.
