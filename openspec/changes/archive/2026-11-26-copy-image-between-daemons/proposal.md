# Proposal

## Why

A build that lives on one engine often has to run on another: a laptop build promoted to a
staging or production engine, or an image moved onto an air-gapped host. The desk can already
write an archive to this machine (`w`, `image-archive-save`) and can push a tag to a registry
(`p`, `image-push`), but it cannot move the image from one connected daemon to another. Today
the operator has to save a tar, switch daemons, leave the desk and run `docker load` by hand —
or insert a registry where none is wanted. The README states this as a non-goal ("nothing acts
on two at once — no copying images or volumes between them"); this change lifts that
limitation for images only.

## What Changes

- Add Images › `C` "copy to daemon…", acting on the highlighted image of the focused daemon
  and one other connected daemon chosen from this session.
- Stream the image archive directly from the source daemon into the target daemon
  (`GET /images/get` → `POST /images/load`) as one background job: no host file, no temporary
  tar on disk, no registry request.
- Keep the source row's naming: a tagged row arrives with the same `repo:tag`; an untagged row
  arrives untagged. The copy never retags; `t` on the target does that afterwards.
- Transfer exactly the platform the image is inspected as, named in the dialog and the job,
  as `w` save archive already does.
- Require a typed confirmation naming both daemons, the reference (or ID), the short image ID,
  the platform and the fact that an existing target tag may be moved by the load.
- Pin both daemons for the duration of the job: neither may be removed from the session while
  the copy runs, and the source tag is re-inspected immediately before the stream starts.
- Verify the result on the target (the loaded image's layer chain and platform must match the
  source; the two stores may name the same build by different IDs) and report bytes streamed,
  target progress and the target's own load messages. A cancel or a failure says what may
  remain on the target: content-addressed layers can remain, and no image or moved tag is
  claimed unless the load completed.
- Refuse the action in read-only mode (the target write is a mutation), when only one daemon
  is connected, when the chosen target is disconnected, and for a selection.
- Extend the engine interface with `LoadImage` and the fake daemon with `POST /images/load`;
  update the README's key table, its "what it does not do" paragraph, and the docs that
  describe image actions.

## Capabilities

### New Capabilities

- `image-copy`: copying one highlighted image from the focused daemon to another connected
  daemon without a registry: the action, target selection, typed confirmation, streaming job,
  progress, cancellation, verification and the two-daemon safety rules.

### Modified Capabilities

- `daemon-session`: the rule that an action and every object it acts on belong to the focused
  daemon gains one exception — a copy names a target member explicitly, and both members are
  held for the job's duration.
- `background-jobs`: a job may span two daemons; it is shown with both names, and removal of
  either daemon is refused while it runs.
- `mutation-safety`: a confirmation for an action involving two daemons names both endpoints,
  and a load that may move an existing target tag is confirmed by typing the tag.

## Impact

- `internal/engine`: new `LoadImage` method on `Engine`, implemented by `mobyEngine`,
  wrapped/refused by `readOnlyEngine`, with load-stream progress decoding and error mapping;
  the shared test doubles must implement it.
- `internal/fakeengine`: `POST /images/load` with realistic JSON progress, image registration,
  tag-move behaviour and settable chunk delay/failure, so engine, plan, UI and e2e tests can
  drive a real two-daemon copy in-process.
- `internal/plan`: a copy plan and its executor that takes two engines (source read, target
  write), pinning, verification and job logging.
- `internal/jobs`: a job can be recorded against a second daemon so both are protected from
  removal and both are named in the Jobs pane.
- `internal/ui`: the Images action, target picker over the session's connected members, typed
  confirmation, streaming job wiring, status/notice handling and target list refresh.
- `internal/session`: exposes what the picker and the removal guard need for a two-daemon job.
- Documentation and help: README action tables, the copy section, "what it does not do", the
  daemon-session section, and `docs/DECISIONS.md` for the choices this change records.
