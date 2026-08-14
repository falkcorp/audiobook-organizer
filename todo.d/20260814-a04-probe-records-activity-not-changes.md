- [ ] **A04's designated probe cannot verify the op-ID audit trail.**
      `maintenance.temp-file-cleanup` routes through
      `sweep.CleanupOrphanedTempFiles`, which records each deletion via
      `activity.LogBatch` — the ACTIVITY feed — and never calls
      `CreateOperationChange`. So even a run that deletes files writes zero
      `operation_changes` rows, and the 2026-08-14 probe (which found 0
      orphans anyway) was doubly inconclusive. To verify #2414 on prod, pick
      a mutating op whose write path actually calls `CreateOperationChange`
      (C513's list of the 8 `ctxOpID` consumers is the menu — read the branch
      first), or decide temp-file deletions SHOULD be operation_changes and
      wire it, making the safest probe also a valid one.
