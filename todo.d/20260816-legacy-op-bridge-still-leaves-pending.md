- [ ] **The `LegacyOpID` bridge still leaves rows at `pending`.** 16 scheduled task
      types pass `schedulerExtraOpParams.LegacyOpID` into their v2 op so the legacy
      row gets closed when the op finishes. It is not working: `purge-deleted`,
      `temp-file-cleanup`, `isbn-enrichment`, `author-dedup-scan`, `archive-sweep`,
      `trash-cleanup` and `tombstone-cleanup` are all in the bridged set AND all
      turned up `pending` in the 2026-08-16 production read. Five *unbridged* tasks
      were fixed separately by removing the legacy row entirely; these 16 still
      create one and rely on a bridge that demonstrably does not close it. Either
      fix the bridge or finish the job and stop writing legacy rows here too —
      preferred, since nothing reads them.

- [ ] **`ClearStaleOperations` exists only to paper over the above.** `POST
      /operations/clear-stale` walks the legacy table and force-marks every
      pending/running/queued row as `failed`. It is a manual broom for rows that
      should never have been left behind. When the legacy table stops being written,
      delete the route.

- [ ] **`CancelOperation` (legacy) has AI-scan handling `CancelOperationV2` lacks.**
      Before deleting `DELETE /operations/:id`, port the branch that resolves an AI
      scan by `scan.OperationID` and cancels it through the pipeline manager —
      otherwise cancelling an AI scan silently stops working.

- [ ] **`/tasks/*` and `/maintenance-window/*` are NOT v1 operations.** Six routes on
      the legacy operations handler are scheduler *configuration*, not operation
      records. They should not be converted to op-defs or deleted with the rest;
      move them to their own handler so "retire v1 operations" does not read as
      "delete task scheduling".
