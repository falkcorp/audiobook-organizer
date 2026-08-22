### 🐛 `LegacyOpID` in maintenance job params defeats the new `EnqueueOp` dedupe

- [ ] **Set `DedupeQueuedRuns: true` on the maintenance-job OperationDefs** (or otherwise
      exclude per-request identity from the dedupe comparison).

  **Where:** `internal/server/maintenance_job_op.go` — `registerMaintenanceJobOp` is the
  single factory for all 37 maintenance defs, so this is a one-line change covering the
  whole family. `OperationDef.DedupeQueuedRuns` is the opt-in field added by PR #2688; it
  is currently set on **zero** defs.

  **Why:** #2688 made `EnqueueOp`'s `ConcurrencyKey` dedupe params-aware — an active op is
  reused only when the marshalled params are byte-equal. But
  `internal/server/maintenance_dispatcher.go:181,190` puts a freshly-generated
  `opID := ulid.Make().String()` into `maintenanceJobOpParams.LegacyOpID` on **every**
  request. Two identical user requests therefore never compare byte-equal, so a
  double-click queues two real runs instead of one. Dispatcher Gate 3 serializes them, so
  there is no concurrency hazard — the job simply runs twice.

  Same shape at `internal/server/reconcile.go:52`, `reconcile.go:131`, and
  `internal/server/duplicates/handler.go:588`.

  **Note this is not a pure regression.** Before #2688 the second click was silently
  swallowed: `EnqueueOp` returned the running op's ID and discarded the new params, which is
  the bug #2688 exists to fix. Neither old nor new behaviour is right. The correct
  behaviour is one run plus a "this is already running" response to the caller.

  **Why not just drop `LegacyOpID` from the struct:** it is load-bearing. The v2 registry op
  needs it to know which legacy `operations` row to update, and `resumeLegacyOp`
  (`server_lifecycle.go`) reads it back on restart. `JobID` in the same struct is already
  documented as retained-for-old-rows only; `LegacyOpID` is not.
