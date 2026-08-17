- [x] **~~The `LegacyOpID` bridge still leaves rows at `pending`.~~ WRONG — the
      bridge worked.** This item was written from a production read of rows that
      all predated the fix. Measured 2026-08-16: the newest `pending` row was
      `01:05:41 -04:00`, the bridge (`5aeb02a8`) landed at `01:19`, and **zero**
      rows created after it are pending. The list is `created_at` DESC and the
      200-row page reaches back to 2026-08-09, so every post-bridge row was in
      the sample — the absence is real, not a sampling artifact. The two rows
      created after the 16:36 restart are both `completed`, at `1/1` with message
      `"completed"`, which is the bridge's own signature.
      **There was a real defect underneath it**, now fixed: `legacyStatusFor`
      enumerated three interrupted variants, and `interruptedStatus` mints
      `interrupted_quiesced` for every resume policy except `ResumeDrop` — three
      of the four legal values — while `worker.go` publishes
      `interrupted_restart`. Unmapped statuses returned early without writing or
      logging, indistinguishable from an op with no legacy row. #2500 had just
      moved `library.scan` to `ResumeRestart`, making the unmapped branch its
      normal outcome across a restart.
      The scheduler now writes no legacy rows at all, so the question is moot for
      it either way.

- [ ] **`ClearStaleOperations` is still wired, deliberately.** `POST
      /operations/clear-stale` force-marks pending/running/queued legacy rows as
      `failed`. It is the only broom for the ~183 historical rows stranded before
      the bridge landed, so deleting it now would remove the only tool for them.
      It is also dishonest for rows whose jobs actually completed — `failed` is
      not what happened. Retire it together with the supervised backfill in
      `todo.d/20260816-backfill-stuck-legacy-op-rows.md`, not before.
      Note `internal/aiscan/pipeline.go` still writes the legacy table directly at
      4 call sites, so "nothing writes it anymore" is not yet true.

- [x] **`CancelOperation` (legacy) had AI-scan handling `CancelOperationV2`
      lacked.** Ported first, route deleted second. The wiring that supplies the
      pipeline manager is not itself asserted — see
      `todo.d/20260816-ai-scan-cancel-wiring-unverified.md`.

- [ ] **`/tasks/*` and `/maintenance-window/*` are NOT v1 operations.** Six routes
      on the legacy operations handler are scheduler *configuration*, not
      operation records. They should not be converted to op-defs or deleted with
      the rest; move them to their own handler so "retire v1 operations" does not
      read as "delete task scheduling". Still outstanding.
