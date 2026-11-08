# Tasks

## 1. The registry

- [x] 1.1 `ListTags` over `/v2/<path>/tags/list?n=100`, following `Link` on the same host and
  path; verify three pages of 250 tags in three requests, and a refused off-host link.
- [x] 1.2 `Describe`: one manifest GET; verify one request, an index's platforms, a single
  manifest, and no child or config read.
- [x] 1.3 The 401 sentence on the tag list and the manifest, anonymous and credentialed, and a
  404 that says no repository; verify the wording and that nothing claims absence.
- [x] 1.4 `ParseRepository`; drop names outside the tag grammar; verify both.

## 2. The browser's knowledge

- [x] 2.1 Natural order; verify a table and that `Less` is a total order on it.
- [x] 2.2 The listing and its cap of 1000; verify ten pages, an oversized page, and no more pages.
- [x] 2.3 The join by `RepoDigests`; verify every outcome, another repository, an image built
  here, a digest-pulled image on both stores, and an image ID that equals the digest.
- [x] 2.4 The daemon's platform and a missing build; verify the uname mapping.
- [x] 2.5 The build-time batch; verify the three-request bound, the epoch, a missing platform,
  the quota stop with what was read kept, and cancellation.

## 3. The pinned pull

- [x] 3.1 `plan.PullPinned`, `TierTyped`, word = tag; verify the reference, the effects, the
  already-here note, the platform refusal and bad input.
- [x] 3.2 Upstream's `P` on a moved verdict opens the same plan; verify, and that `P` elsewhere
  is still the pull form.

## 4. The view

- [x] 4.1 `B`, the name line, prefill, restore on close; verify no request with no image, every
  key as text, Esc's order.
- [x] 4.2 One request a highlighted row; verify resting, returning, passing quickly, reopening.
- [x] 4.3 `Tab`, `/`; verify the second page's query and the filter.
- [x] 4.4 `d` and `c`; verify the quota stop in the view.
- [x] 4.5 Enter; verify the typed-tag dialog, the full reference, the request the fake daemon
  receives, and `here` afterwards.
- [x] 4.6 Read-only and `--no-registry`; verify no mutation, no request.
- [x] 4.7 Layouts at 80×24, 120×40 and 200×50, every theme, read-only.

## 5. The sandbox and the documents

- [x] 5.1 `fakeregistry` `tags/list`, `Listen`; `fake-dockerd -registry`; `make sandbox`.
- [x] 5.2 End-to-end in the real binary; capture `docs/screens/39`–`41`.
- [x] 5.3 README (keys, `B`, the section after Upstream, what it does not do), help,
  `docs/DECISIONS.md` round twelve.
- [x] 5.4 `go build`, `go vet`, `go test ./...`; `make race` compared with `HEAD`.
