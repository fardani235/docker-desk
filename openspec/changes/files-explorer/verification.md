# Verification against the real daemon (task 6.3)

Read-only, on the machine this change was built on (Docker Engine 29.9.3, API 1.56, the
containerd image store, `unix:///var/run/docker.sock`), on 2026-11-11.

## Before and after

A snapshot of the daemon's objects was taken before the first check and after the last —
`docker ps -a --no-trunc` (ID, name, image, state, created), `docker images --no-trunc --digests`
(ID, reference, digest, created), `docker volume ls`, `docker network ls --no-trunc`: 18 lines,
4 containers (2 running, 2 exited), 5 images. **The two snapshots are identical**, compared after
each of the four runs below.

## `make real-readonly-check` (engine, read-only; GET/HEAD enforced by the HTTP hook)

`TestRealDaemonFilesReadOnly`, added by this change:

- The smallest image, `gitsearch-platform-backend:latest` (`4155fad99b21`, the daemon's size
  284 MB), read in `ModeFiles`: OCI layout, **8 layers, as inspect says**, 66.6 MB of archive read
  in 3.4 s; **10,330 entries indexed, every one the archive holds**; the tree's files add up to
  186 MB beside the daemon's 284 MB — shown as different measurements. No finding and no package
  list were made.
- Every container's `ContainerChanges` against the real `docker diff`, both sorted:
  `gitsearch-platform-frontend-1` (running) 4 changes, `gitsearch-platform-backend-1` (running) 39,
  `agitated_fermat` (exited) 1,889, `gitlab` (exited) 917 — **all four agree, 2,849 changes**.
- Found on the way: the daemon returns a change list in no fixed order (two calls differ), and the
  CLI prints the order its call got. The first version of the check compared the lists as sent and
  failed on three containers whose sets were identical; it now compares them sorted. The view was
  never affected — `files.Apply` sorts before it lays changes over the tree (DECISIONS V14).

The other real-daemon checks (`TestRealDaemonReadOnly`, `TestRealDaemonExportReadOnly`,
`TestRealDaemonDiskUsageReadOnly`) pass; `TestRealDaemonPackagesReadOnly` skips, as before: its
rpm image is not on this daemon.

## The desk itself, `--read-only` (`e2e.TestRealDaemonFilesTour`, added by this change)

`DOCKER_DESK_REAL_FILES_CONTAINER=gitsearch-platform-backend-1`: `F` showed "39 listed by the
daemon" and "the image's files have not been read" at once; `Enter` read the archive (one job) and
drew the overlay — "14 added · 15 changed; sizes and layers are the image's", "10 changes beneath a
directory the container added: not drawn, its contents are not read", "10,330 entries indexed",
the two sizes side by side. The rows matched `docker diff` by eye: the `__pycache__/` directories
Python wrote are marked added, show no size and do not open; `/app/app/` and its parents are marked
changed; the image's files keep their sizes and layer 8. A filter (`etc`) counted its matches
among the paths searched. Exit code 0.
