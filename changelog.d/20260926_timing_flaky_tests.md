### Fixed

- Two timing-flaky tests no longer depend on wall-clock scheduling: `TestScanStandDownCheckpoint_SlowLoopLongerThanLeaseKeepsHold` drives the lease with the injectable `ScanStandDownNow` clock, and `TestCache_SlowPatchIsBoundedByWait` parks the patch on a channel instead of timing a sleeping comparator.
