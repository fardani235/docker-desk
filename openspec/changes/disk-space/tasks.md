## 1. Engine

- [x] 1.1 `DiskUsage`, `ImageSizes`, `ImageLayers`, `ContainerSizes` (read methods) and their types; legacy df flagged; verify with `TestReadOnlyCoversEveryMutation`, `TestDiskUsageAsTheDaemonStatesIt`, `TestLegacyDiskUsageIsMarkedDerived`

## 2. The fake daemon

- [x] 2.1 Layers shared by chain ID; history with an empty-layer entry; `shared-size`; `size=1`; `/system/df` in the current and legacy shapes; volume sizes; build cache; delays; demo images share base layers
- [x] 2.2 `scripts/cli-crosscheck.sh`: `docker system df` against the fake

## 3. Accounting

- [x] 3.1 `internal/space`: chain IDs, history mapping by exact count, index, groups listed once, what a set frees (exact, at least, unknown), consistency with the daemon's figures; verify with `TestChainIDs`, `TestHistoryMapsOnlyByExactCount`, `TestSharedBytesAreListedOnce`, `TestWhatASetFrees`, `TestAnIndexThatDisagreesWithTheDaemonIsNotUsed`

## 4. The view

- [x] 4.1 Open, close, restore; layout and scroll at 80×24 and 120 columns; verify with `TestSpaceOpensAndClosesWhereItWas`, `TestSpaceFitsAndScrolls`
- [x] 4.2 Totals, images, shared groups, alone and together; verify with `TestSpaceTotalsAreTheDaemons`, `TestSharedImageFreesLessAloneThanItsSize`
- [x] 4.3 Writable-layer job; verify with `TestWritableLayersOnlyWhenAsked`, `TestSpaceMeasurementsAreCancellable`
- [x] 4.4 Archive attribution from the cache; verify with `TestArchiveAttributionReadsNoArchive`
- [x] 4.5 Provenance, age, stale; verify with `TestFiguresTurnStaleAfterAChange`
- [x] 4.6 Enter jumps; verify with `TestEnterGoesToTheObject`
- [x] 4.7 `d` and `X` through the existing dialogs, freed bytes in confirmations; verify with `TestRemovalConfirmationStatesWhatItFrees`, `TestRemovalConfirmationWithoutAnAccountSaysSo`, `TestSpaceReclaimUsesTheExistingDialogs`
- [x] 4.8 Read-only, older daemons, layer sizes that do not add up; verify with `TestSpaceInReadOnlyMode`, `TestSpaceOnAnOlderDaemon`, `TestSpaceWhenLayerSizesDoNotAddUp`
- [x] 4.9 Hints and help (`TestSpaceInReadOnlyMode` checks both offer no reclaim)

## 5. Proof and documentation

- [x] 5.1 End to end: `TestSpace` at 120×40 and 80×24 `--read-only`; screens 26, 27 and 28; totals compared with the real `docker system df` on the same fake
- [x] 5.2 Opt-in real-daemon read-only account (`TestRealDaemonDiskUsageReadOnly`, `make real-readonly-check`)
- [x] 5.3 README (the gap note replaced), DECISIONS U1–U13, TESTING round five, JOURNAL round five
