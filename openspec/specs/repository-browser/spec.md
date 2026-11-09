# repository-browser Specification

## Purpose
Defines the repository browser: listing one registry repository's tags, describing the
highlighted one, joining it with the local images, reading build times on request, the pinned
pull by digest, and what the view refuses to claim.

## Requirements

### Requirement: The browser opens on a repository and asks only for its tag list
The desk SHALL open a full-screen repository browser with `B` from any pane, and SHALL restore
the focus, the kind and the highlight when it closes. When an image is highlighted the name line
SHALL be filled with that image's repository and the tag list SHALL be asked at once; otherwise
the view SHALL open on an empty name line and SHALL send no request. Opening SHALL ask for one
page of tags and nothing else.

#### Scenario: Opened on an image
- **WHEN** the operator highlights `host/acme/api:3.28.2` and presses `B`
- **THEN** one `GET /v2/acme/api/tags/list?n=100` is sent, and no manifest or blob is read until a row is highlighted

#### Scenario: Opened with no image
- **WHEN** the operator presses `B` with no image highlighted
- **THEN** the view shows an empty name line and no registry request is sent

#### Scenario: Closed
- **WHEN** the operator closes the view with `B` or `Esc`
- **THEN** the pane, the kind and the highlighted row are as they were when it opened

### Requirement: Tags are paged by the registry's link and held up to a cap
The desk SHALL list tags with `GET /v2/<path>/tags/list?n=100`, SHALL ask for the next page only
when the operator presses `Tab`, and SHALL follow only the page the registry's `Link` names on
the same host and path. It SHALL hold at most 1000 tags and SHALL say when the registry has
more. It SHALL NOT search, read `_catalog`, or contact `hub.docker.com`.

#### Scenario: The next page
- **WHEN** the registry answers with a `Link` to `?n=100&last=t099` and the operator presses `Tab`
- **THEN** exactly that page is asked for and its tags are added

#### Scenario: A link elsewhere
- **WHEN** the registry's `Link` names another host
- **THEN** it is not followed and the view says so

#### Scenario: The cap
- **WHEN** 1000 tags are held and the registry names another page
- **THEN** no further page is asked for and the view says the registry has more

### Requirement: Tags are in text order, never recency
Tags SHALL be shown in natural text order — digit runs compared by value — and the view SHALL
say that the order is text order and not recency.

#### Scenario: Version-like tags
- **WHEN** the registry lists `3.10`, `3.9` and `3.28.3`
- **THEN** they are shown as `3.9`, `3.10`, `3.28.3`, under a line saying the order is text order, never by recency

### Requirement: The highlighted row is described with one request and kept
When the highlight rests on a row the desk SHALL send one manifest `GET` for that tag and SHALL
NOT walk to a platform manifest or a config. The answer — digest, media type, an index's
platforms — SHALL be kept for the session by host, path and tag with the time it was obtained.
A row with a kept answer SHALL NOT be asked again unless the operator asks.

#### Scenario: Moving through rows
- **WHEN** the operator rests on one row, moves to another, and returns
- **THEN** exactly two manifest requests have been sent

#### Scenario: Passing rows quickly
- **WHEN** the operator moves through several rows without resting
- **THEN** only the row the highlight rests on is asked for

### Requirement: The remote digest is joined with the local images by digest
The desk SHALL compare a tag's remote digest with the `RepoDigests` of local images of the same
repository, and SHALL NOT compare it with an image ID. The row SHALL say `here`, `here, the tag
moved upstream`, `same build as <tag>`, or `-`.

#### Scenario: Present
- **WHEN** a local image of the repository records the digest under this tag, or under no tag
- **THEN** the row says `here`

#### Scenario: The tag moved
- **WHEN** a local image carries the tag but records another digest
- **THEN** the row says `here, the tag moved upstream`

#### Scenario: The same build under another tag
- **WHEN** a local image records the digest under another tag of the repository, `stable`
- **THEN** the row says `same build as stable`

### Requirement: A missing platform is said before any pull
When a tag names an index that publishes no build for the daemon's platform, the row SHALL say
so, and the pull confirmation SHALL refuse it.

#### Scenario: An arm64-only tag on an amd64 daemon
- **WHEN** the highlighted tag's index lists only `linux/arm64/v8` and the daemon reports `x86_64`
- **THEN** the row says there is no amd64 build and Enter opens a refusal that sends nothing

### Requirement: Build times are read on request, as one job that stops at a quota
`d` SHALL read the build times of the rows on screen as one cancellable job, with up to three
requests a row, choosing the daemon's platform, and SHALL name the platform whose config each
time came from. A quota refusal SHALL stop the job and keep what was read. Nothing SHALL poll.

#### Scenario: A quota refusal midway
- **WHEN** the registry answers 429 on the third row
- **THEN** the job stops, says when the quota resets if the registry said, and the first rows keep their times

### Requirement: A 401 does not claim absence
A 401 from the registry on the tag list or a manifest SHALL be reported as "the registry
answered 401 - it does not have this repository, or this credential is not allowed to see it",
and SHALL NOT be described as a missing repository.

#### Scenario: A private or absent repository
- **WHEN** the registry answers 401 after the challenge was answered
- **THEN** the view shows that sentence, and nothing on screen says the repository does not exist

### Requirement: The filter and the name line take every key
`/` SHALL filter the loaded rows. `e` SHALL focus the name line; while it or the filter is
typed in, every key SHALL be text until Enter or Esc. Esc SHALL step out of the line, then clear
a filter, then close the view.

#### Scenario: Typing a name containing action keys
- **WHEN** the operator presses `e` and types `qdB`
- **THEN** the name line holds `qdB` and nothing quits, reads or closes

### Requirement: Refusals
Under `--no-registry` the view SHALL refuse to open and SHALL say why. In read-only mode browsing
SHALL work and the pull SHALL be refused by the desk's existing read-only guard.

#### Scenario: --no-registry
- **WHEN** the desk runs with `--no-registry` and the operator presses `B`
- **THEN** the view does not open, the status line says why, and no registry request is sent

#### Scenario: Read-only
- **WHEN** the desk runs with `--read-only` and the operator presses Enter on a described row
- **THEN** no dialog opens, the status line says the action is disabled, and nothing is sent to the daemon
