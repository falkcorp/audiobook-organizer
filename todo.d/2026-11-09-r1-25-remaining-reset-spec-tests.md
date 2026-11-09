- [ ] **PLATFORM-CORE-R1-25-RESET-SPEC-TESTS** Four rows of 05-storage
      §5S.3.15's factory-reset table that R1-25 did not write under their
      spec names (each needs fault injection in the reset swap):
      `TestFactoryResetSetAsideFailureKeepsStore` (a rename failure before
      `set_aside_started` keeps the live store and moves completed targets
      back; core's `TestRenameUndoneWhenItsDestinationSyncFails` and
      `TestResetResumeMovesAJournaledTargetAgain` cover parts),
      `TestFactoryResetCrossDeviceTargets` (state directory and Open
      Library store on other filesystems, or an injected EXDEV; core's
      `TestSetAsideDestStaysOnOwnFilesystem` and
      `TestStageRefusesUnrenamableTargets` cover the staging half),
      `TestFactoryResetResumeAfterExpiryCompletes` (a crash after some
      moves, then a boot after `expires_at` and by another binary, resumes
      to `set_aside`), and `TestRollbackRefusesInflightReset` (the rollback
      guard; R1-27's `make rollback-restore` owns it).
