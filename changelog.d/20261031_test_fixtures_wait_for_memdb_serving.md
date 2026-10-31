### Fixed

- Two CI-only flakes, `TestApplyMultidisc_RealCombineOfARowlessMemberAtAHeldPathReportsIt`
  and `TestPreflightUndoConflicts_RetireRowsOnPurgedShell`, failed with "memdb is
  not serving" on loaded Woodpecker agents. Their fixtures (`newApplyTestStore`,
  `newRetireStore`) opened a raw `database.NewPebbleStore`, which starts the memdb
  warmup asynchronously, so the test body could run before memdb published and the
  production fail-closed ownership lookups (`BookFilesAtPath`, the retire and
  combine checks) refused. Both fixtures now use `dbtest.NewStore`, which returns
  only once memdb serves. Production fail-closed behaviour is unchanged.
- `dbtest.NewStore` now fails the test if warmup finishes without memdb serving
  (a failed warmup also closes `warmupDone`), and bounds the wait at two minutes,
  instead of silently handing back a store on its Pebble fallback.
- Added `database.SetWarmupStartHookForTest`, a test seam that delays the warmup
  goroutine so the "test runs before memdb serves" window can be reproduced
  deterministically; regression tests pin both fixtures and `dbtest.NewStore`
  under a delayed warmup.
