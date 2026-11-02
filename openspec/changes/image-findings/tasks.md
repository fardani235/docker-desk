## 1. Engine

- [x] 1.1 `SaveImage` (read method), `Platform`, `ImageDetail.Variant`, `Container.NetworkMode`; platform omitted below API 1.48; verify with `TestReadOnlyCoversEveryMutation`, `TestReadOnlyExportsAnImage`, `TestAPIOlder`

## 2. Fixtures and the fake daemon

- [x] 2.1 `internal/forensics/fixture`: classic and OCI layouts, gzip layers, whiteouts, opaque dirs, xattrs, history, nested index with attestations and absent blobs, minimal ELF and Go build info; `Demo` and `Plain` images
- [x] 2.2 Fake daemon serves `GET /images/get` and `/images/{name}/get`, `SetImageArchive`, `SetImageSize`, `ExportChunkDelay`; privileged / network mode / user on containers; demo seeds `shop/api:dev`

## 3. Analyser

- [x] 3.1 Streaming reader: classification by content, bounded JSON, layer walk with digests and diff IDs, cancellation, progress; verify with `TestBothLayoutsSayTheSame`, `TestCancellation`, `TestProgressAndBounds`
- [x] 3.2 Manifest, config, history mapping, index and attestations; verify with `TestIndexOnlyArchive`, `TestAttestationsAndAbsentBlobs`, `TestInstructionsAreOmittedWhenTheHistoryDoesNotMap`
- [x] 3.3 Rules: config, layer, filesystem, secrets, binaries, supply; verify with `TestDeletedSecretIsStillInTheImageBytes`, `TestConfigRules`, `TestFilesystemAndBinaryRules`, `TestLayerRules`, `TestPerRuleCap`, `TestPackagedExamplesAreHints`
- [x] 3.4 Masking and the `Secret` type; errors in the desk's words; verify with `TestSecretsNeverPrint`, `TestBrokenInputFailsInItsOwnWords`
- [x] 3.5 Live join; verify with `TestLiveFindings`

## 4. The tab

- [x] 4.1 Findings tab: job, cache, consent, cancel, render at every size; verify with `TestFindingsTabRendersAndScrollsAtBothLayouts`, `TestSecondOpenIsInstant`, `TestRemoteLargeImageAsksFirst`, `TestAnalysisIsACancellableJob`, `TestMovingAwayCancelsTheRunningAnalysis`, `TestFrameGeometryAtEverySize`
- [x] 4.2 Cursor, `o`, `v`; reveal one at a time; verify with `TestRevealIsOneAtATimeAndHidesWhenTheCursorMoves`, `TestOrderCycles`, `TestASecretNeverLeavesTheScreen`
- [x] 4.3 Live join in the tab and read-only mode; verify with `TestLiveJoinFollowsTheDaemon`, `TestFindingsInReadOnlyMode`
- [x] 4.4 Hints and help; verify with `TestFindingsHintsAndHelp`

## 5. Proof and documentation

- [x] 5.1 End to end: `TestImageFindings` at 150×42 and 80×24 `--read-only`; screens 24 and 25
- [x] 5.2 Opt-in real-daemon export (`TestRealDaemonExportReadOnly`, `make real-readonly-check`)
- [x] 5.3 README, DECISIONS F1–F14, TESTING round four, JOURNAL round four
