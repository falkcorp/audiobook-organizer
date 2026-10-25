### Fixed

- Fixed two flaky reconcile cancel tests,
  `TestBuildReconcilePreviewCtx_CancelDuringStatPassFailsTheScan` ("stat pool
  kept starting stats after cancel: 362 of 3000 ran" on a slow `-race` CI
  runner) and `TestHashFilesConcurrent_CancelStopsStartingHashes`, which had the
  same shape. The pools were correct (every worker checks `ctx` before it
  stats or hashes); the tests' heartbeat hooks called `cancel()` outside any
  lock, so a descheduled canceling goroutine let other workers keep working
  before the context was canceled at all, and the tests counted those as work
  started after cancel. The hooks now cancel under a lock and count only beats
  that began after the cancel returned, and the tests assert that count stays
  below the pool limit instead of a share of the total.
- The worker-pool size used by the reconcile scan's stat, hash and shard pools
  and by the primary election now comes from one helper, `cpuPoolLimit()`,
  instead of five copies of `max(runtime.NumCPU(), 1)`; the tests read the same
  value. The limit itself is unchanged.
