- [ ] **SCAN-FAIL-RESET-SYNC** Every successful file parse in a scan does a
      synced Pebble delete, even when the file has no scan-fail counter.
      `scanner.resetScanFailCount` runs after every successful read in
      `ProcessBooksParallel`, and `PebbleStore.ResetScanFailCount`
      (`internal/database/pebble_store_quarantine.go`) is
      `p.db.Delete(key, pebble.Sync)`. A full-library scan re-reads tens of
      thousands of files, so that is one fsync per file to delete keys that
      almost never exist: the counter only exists for a file that failed. Done
      means: measure the cost on a real scan first. If it matters, delete only
      when a counter exists (a `Get` is cheap), or use `pebble.NoSync` for the
      reset. A lost reset only means one extra counted failure toward
      auto-quarantine, so keep `Sync` on `IncrScanFailCount`, where it is the
      quarantine evidence. Whatever changes, keep the reset's error logging
      added in PR #3701.
