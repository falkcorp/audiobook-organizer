### 🐛 Maintenance jobs have no `ConcurrencyKey`, so nothing dedupes or serializes them

- [ ] **Decide whether the 37 maintenance jobs should serialize against themselves**, and if so
      give each def a per-job `ConcurrencyKey` (the op ID is the natural key) **plus**
      `DedupeQueuedRuns: true`.

  **Where:** `internal/server/maintenance_job_op.go` — `registerMaintenanceJobOp` is the single
  factory for all 37 defs, so both fields are set in one place. `internal/maintenance/job.go:131`
  (`DefaultPolicy`) is where `ConcurrencyKey: ""` is hardcoded, and `job.go:123` explicitly defers
  per-job keys to "PR-2".

  **The actual state of things.** Two gates both test `def.ConcurrencyKey != ""`:
  `EnqueueOp`'s dedupe block (`registry.go`) and dispatcher Gate 3 (`dispatcher.go:107`). Every
  maintenance job uses `DefaultPolicy()`, whose `ConcurrencyKey` is `""`. So **neither gate has
  ever applied to a maintenance job**: a double-click starts two runs, and they run
  *concurrently*, not serialized.

  **Correction to an earlier note (2026-08-22).** A previous version of this fragment claimed
  PR #2688 (params-aware `EnqueueOp` dedupe) turned a silently-swallowed double-click into two
  serialized runs, and that setting `DedupeQueuedRuns: true` would restore single-run behaviour.
  **Both claims are wrong**, because they assume execution reaches a branch these defs never
  enter. #2688 changed nothing for the maintenance family. `DedupeQueuedRuns` alone would be
  inert. The error came from accepting a subagent's report without checking the gate condition it
  depended on.

  **What IS true about `LegacyOpID`.** `maintenance_dispatcher.go:153` generates a fresh
  `opID := ulid.Make().String()` per request and puts it in `maintenanceJobOpParams.LegacyOpID`
  (lines 181, 190), so two identical requests never marshal byte-equal. That defeats #2688's
  byte-equality dedupe *for any def that reaches it* — so it must be dealt with as part of the
  work above, not before it. Same shape at `reconcile.go:52`, `reconcile.go:131`,
  `duplicates/handler.go:588`.

  Do not simply drop `LegacyOpID` from the struct: the v2 op needs it to find the legacy
  `operations` row to update, and `resumeLegacyOp` (`server_lifecycle.go`) reads it on restart.
  `JobID` in the same struct is documented as retained-for-old-rows only; `LegacyOpID` is not.

  **Why it matters:** `cleanup-empty-folders` removes directories from disk, and seven jobs are
  both `CanResume()` and advertise `dry_run: true`. Two concurrent runs of a mutating job is the
  failure mode worth closing.
