## 1. Planner

- [x] 1.1 Add `Plan.Skipped`, `plan.Member`, `plan.Skip`, `plan.SetCapable` and `plan.Set` reusing the single-object constructors; verify with `TestSetHoldsExactlyTheRunnableMembers`, `TestSetOnlyForSetCapableOperations`
- [x] 1.2 Collect `NotApplicable` reasons and blockers into `Skipped`; nothing-runnable sets say so; verify with `TestSetLeavesOutWhatTheSingleRulesRefuse`, `TestSetWithNothingRunnableCannotRun`
- [x] 1.3 Typed tier with word = verb + count; `TotalSize` = sum of step sizes; image rows grouped so every reference selected deletes the image; verify with `TestImageSetSizesAndReferences`, `TestImageSetOfEveryReferenceDeletesTheImage`
- [x] 1.4 Run a set through the unchanged `Execute`, partial failure reported; verify with `TestSetExecutesAndReportsPartialFailure`

## 2. Selection in the List pane

- [x] 2.1 `Model.marks` keyed by row key, reconciled on rebuild (refresh, drop vanished, only when loaded); verify with `TestSelectionSurvivesRefreshAndDropsVanishedObjects`
- [x] 2.2 `Space`, `a`, the Esc ladder, gutter marker, title count; compose not selectable; Space still pages elsewhere; verify with `TestSelectAllEscAndSpaceElsewhere`

## 3. Acting on a selection

- [x] 3.1 Set-capable actions build a set plan; shell/connect/disconnect/prune refuse; pull/new ignore; menu offers what applies; verify with `TestSetPlanHoldsExactlyTheSelectedRunnableObjects`, `TestSingleObjectActionsRefuseASet`
- [x] 3.2 Read-only refuses set actions; verify with `TestReadOnlyRefusesASet`
- [x] 3.3 Dialog: "Not included" block, set prompt, headline verbs; selection cleared when the job starts; verify with `TestInUseObjectsAreNotIncludedWithTheirReason`, `TestSetConfirmationMustTypeTheCount`, `TestPartiallyFailingSetJobReportsSucceededAndFailed`
- [x] 3.4 80×24 layout with a long remote endpoint; verify with `TestSetConfirmationFitsTheSmallestTerminal`

## 4. Documentation

- [x] 4.1 README key table and safety model name the gesture; help overlay updated
- [x] 4.2 `docs/DECISIONS.md` S1–S8
