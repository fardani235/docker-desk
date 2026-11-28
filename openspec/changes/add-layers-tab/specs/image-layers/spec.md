# Spec Delta

## Purpose

Defines Images › Layers: one row per build step of an image, what each step made, and which
images on the same daemon hold the same layer content.

## ADDED Requirements

### Requirement: One row per build step
The Images › Layers tab SHALL list every step the image's history records as one row, in build
order, oldest first, so the first layer is `L1` as in Findings and Files. A step that made no
layer SHALL be a row too. Each row SHALL carry the step's instruction, its size, the files it
wrote and, once the layer read is made, the number of other images holding the layer it made. A
figure SHALL be left out only when the width cannot hold it or when it is not known yet.

#### Scenario: Opening the tab
- **WHEN** the operator opens Layers on an image with 18 steps and 7 layers
- **THEN** the tab lists 18 rows oldest first, the base step at the top, and the rows that made a layer name `L1` to `L7`

#### Scenario: A step that made no layer
- **WHEN** a step is `ENV NGINX_VERSION=1.31.9` and the config marks it `empty_layer`
- **THEN** it is a row of its own, marked as one that made no layer, with no size attributed to it

#### Scenario: A narrow terminal
- **WHEN** the pane is 60 cells wide
- **THEN** the files and size columns may be dropped, the instruction stays, and the highlighted step's block still shows every figure

### Requirement: The step-to-layer binding
A step SHALL be bound to a layer only from evidence that cannot be wrong: the archive's config
history, where `empty_layer` says which steps made a layer, or the daemon's history when the
number of entries with a non-zero size equals the number of layers. Otherwise the binding SHALL
be unknown, with the reason. The daemon's `EmptyLayer` field SHALL NOT be used.

#### Scenario: The archive has been read
- **WHEN** the analysis has finished and its config history has 18 entries, 7 without `empty_layer`, and the image has 7 layers
- **THEN** each of the 7 is bound to its layer in order, and the other 11 say they made none

#### Scenario: Only the daemon's history, and the counts agree
- **WHEN** the archive has not been read and exactly 7 of 18 history entries carry a non-zero size for 7 layers
- **THEN** each row with a size names its layer and each row without one says it made no layer, and the tab says the binding came from the counts

#### Scenario: Only the daemon's history, and the counts disagree
- **WHEN** the archive has not been read and the count of entries with a size differs from the number of layers
- **THEN** no row names a layer and the tab says the daemon's history does not say which step made which, and that the archive's history would

#### Scenario: The daemon says every step made a layer
- **WHEN** the daemon reports `EmptyLayer` false for every one of 18 entries of a 7-layer image
- **THEN** the tab does not conclude that 18 steps made layers

### Requirement: The highlighted step's instruction
For the highlighted step the tab SHALL show the whole instruction, one line, with build-argument
values left out and secret-shaped text masked, through the same rule Findings and Changes use. It
SHALL show the layer's content digest and whether it matches the config's `diff_id`.

#### Scenario: A RUN with sealed build arguments
- **WHEN** the step is `RUN |2 TOKEN=s3cr3t make build`
- **THEN** the block names the argument and the command and holds no value of `TOKEN`

#### Scenario: A verified layer
- **WHEN** the layer's diff ID equals the config's `diff_id` for it
- **THEN** the block says the digest matches the config

#### Scenario: A step that made no layer
- **WHEN** the highlighted step is config only
- **THEN** the block says it changed the image's configuration and not its files, and carries no digest

### Requirement: The highlighted step's contents
For a step that made a layer the tab SHALL show the bytes its files hold, the bytes the archive
stores for it, the files and directories it wrote, the paths it deleted and the opaque
directories it set, and the findings about that layer. Where the archive has not been read, it
SHALL show what the daemon's history gives and say the rest is not known yet.

#### Scenario: Before the archive is read
- **WHEN** the tab is drawn while the analysis is still running
- **THEN** the block shows the daemon's size for the step and says the contents are not known yet

#### Scenario: After the archive is read
- **WHEN** the analysis has finished on a layer that added 1,204 files and deleted 3 paths
- **THEN** the block states both counts, the two byte figures and the findings on that layer

#### Scenario: A layer the archive could not read
- **WHEN** the layer's contents could not be walked
- **THEN** the block says so with the reason and claims no file count or byte figure for it

### Requirement: Sharing is by layer content
The tab SHALL count a layer as shared when another image on the same daemon holds a layer with the
same diff ID, whatever its depth. The image itself SHALL never count, and an image holding the same
diff ID more than once SHALL count once. A holder SHALL be named by its reference, or by short ID
when untagged, and a list too long for the pane SHALL say how many more there are.

#### Scenario: A shared base
- **WHEN** `nginx:latest` and `redis:latest` hold the same first-layer diff ID
- **THEN** both layers say they are held by one other image and name it

#### Scenario: The same content at two depths
- **WHEN** an image holds the empty layer's diff ID at five depths and another image holds it at one
- **THEN** each of the five says it is held by that one other image, and the other image counts it once

#### Scenario: Many holders
- **WHEN** a base layer is held by 40 images
- **THEN** the row says 40 and the block names as many as the pane holds and how many more there are

### Requirement: The summary line
The tab SHALL state how many of the image's layers another image here holds and the bytes of the
layers no other image holds, and SHALL name the source of each figure. Until the layer read is
made it SHALL say the holders have not been read and name the key that reads them. When an image
was not read it SHALL say `at least` and how many images that covers.

#### Scenario: A shared base above an application
- **WHEN** 2 of 7 layers are held elsewhere and the other 5 hold 126MB
- **THEN** the summary states both figures and that the layer sizes came from the daemon or the archive

#### Scenario: An image no other image shares
- **WHEN** no other image holds any of its layers
- **THEN** the summary says so rather than showing zero of zero

#### Scenario: An incomplete read
- **WHEN** the layers of 3 images could not be read
- **THEN** the holders and the own figure are stated as `at least`, with the count of images not read

### Requirement: The engine-wide read starts when asked
Reading every image's layers SHALL start only when `s` is pressed, SHALL state how many images that
reads before it starts, and SHALL run as one cancellable job. Opening the tab again on another
image SHALL read only the images not read before.

#### Scenario: Pressing s
- **WHEN** the operator presses `s` on an engine listing 412 images
- **THEN** the tab says it is reading the layers of 412 images, one inspect each, and the Jobs pane shows the progress

#### Scenario: A second image
- **WHEN** the read has finished and the operator opens Layers on another image
- **THEN** the sharing figures appear without a new request for the images already read

#### Scenario: Cancelled
- **WHEN** the read is cancelled in the Jobs pane
- **THEN** the tab says the holders are not known and `s` starts it again

### Requirement: The analysis is the Findings analysis
The tab SHALL draw the report the Findings analysis makes, through the same job, cache and consent
rule: an image already analysed SHALL draw without a request, a remote image over the threshold
SHALL wait for Enter, and `c` SHALL cancel the analysis. The tab SHALL send no request that
changes anything and SHALL work in read-only mode.

#### Scenario: Findings was opened first
- **WHEN** the operator opens Layers on an image already analysed on the Findings tab
- **THEN** the exact steps are drawn in the same frame and no archive request is sent

#### Scenario: A large remote image
- **WHEN** the image is on a remote daemon and larger than the threshold
- **THEN** the tab names the size and the daemon and starts nothing until Enter

#### Scenario: Read-only
- **WHEN** the desk runs with `--read-only`
- **THEN** the steps, the sharing and the analysis work, and no mutating request is sent

### Requirement: Nothing is claimed that is not known
Every figure SHALL name its source, and what cannot be known SHALL be said with its reason: a
history that does not bind, an image whose layers were not read, an analysis that failed, or a
layer the archive could not read.

#### Scenario: A failed analysis
- **WHEN** the analysis ends without a report
- **THEN** the tab says why and Enter retries it

#### Scenario: An image with no history
- **WHEN** the daemon reports no build history for the image
- **THEN** the tab says so in one line, lists no steps, and still shows what is shared when the layers are known

#### Scenario: An image with no layers
- **WHEN** the image has no layers at all
- **THEN** the tab says so rather than drawing an empty list

#### Scenario: The image's own layers cannot be read
- **WHEN** the inspect or the history for the highlighted image fails
- **THEN** the tab says why, lists no step, and `s` reads it again

### Requirement: Keys
`up` and `down`, `j` and `k`, `g`, `G`, `PgUp`, `PgDn` and space SHALL move the cursor; Enter SHALL
give the consent the analysis waits for and retry a failed one; `c` SHALL cancel the analysis; `s`
SHALL start the layer read; `W` SHALL write the build. The tab SHALL be reachable with `[` and `]`
and SHALL sit after Upstream in the strip.

#### Scenario: The strip
- **WHEN** the Images tabs are drawn
- **THEN** the strip reads Info, Inspect, Findings, Upstream, Layers, and the existing tabs keep their positions

#### Scenario: The cursor
- **WHEN** the operator presses `G` and then `g`
- **THEN** the last step is highlighted, then the first

### Requirement: The build can be kept
`W` on Layers SHALL write every step with the facts the highlighted-step block states, not only the
rows in view, under the text-capture rules. The file SHALL name the image and each step's layer.

#### Scenario: Scrolled
- **WHEN** the operator captures the tab while the last step is in view
- **THEN** the file holds the first step too

#### Scenario: Without the archive
- **WHEN** the tab is captured before the analysis has finished
- **THEN** the file holds the daemon's steps and says the contents were not read
