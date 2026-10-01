- [ ] **FLAKE-SCANLOCK-PENDING** `TestPendingBlocksOnlyTheIdleAcquires`
      (`internal/scanlock/scanlock_test.go:173`, "table not empty: 2") failed
      in CI on PR #3639, which does not touch scanlock. It passes 50/50 under
      `-race` locally, so the table check races the release cleanup under CI
      load. Wait for the table to drain instead of checking once.
