### Fixed

- Fixed a flaky `TestBuildReconcilePreviewCtx_CancelDuringStatPassFailsTheScan`
  that failed on slow `-race` CI runners with "stat pool kept starting stats
  after cancel: 362 of 3000 ran". The reconcile stat pool was correct (every
  worker checks `ctx` before it stats); the test's heartbeat recorder called
  `cancel()` after releasing its lock, so a descheduled canceling goroutine let
  other workers keep statting before the context was canceled at all, and the
  test counted those as stats started after cancel. The recorder now cancels
  under its lock and counts only beats that began after the cancel returned,
  and the test asserts that count stays below the pool limit instead of an
  arbitrary share of the total. No production code changed.
