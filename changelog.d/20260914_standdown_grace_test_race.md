### Fixed

- `TestScanStandDown_GraceHoldsQueuedScanThenDispatchesOnce` no longer
  flakes with "scan started 0 times; want 1". The worker marks the op
  `running` before it calls the op's `Run`, where the test counts starts, so
  a loaded CI runner could read the counter in between. The test now waits
  for the start, then checks that the scan started exactly once. It failed
  CI on two unrelated PRs (#3418, #3421) on 2026-09-14. Test-only change.
