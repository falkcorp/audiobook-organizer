### 🐛 `LegacyOpID` defeats `EnqueueOp`'s dedupe for maintenance jobs (serialization fixed 2026-08-22)

- [x] ~~**Decide whether the 37 maintenance jobs should serialize against themselves**, and if so
      give each def a per-job `ConcurrencyKey` (the op ID is the natural key) **plus**
      `DedupeQueuedRuns: true`.~~ — **decided and shipped 2026-08-22 (PR #2709).**
      `registerMaintenanceJobOp` now derives `ConcurrencyKey` from the job's own op ID when the
      job's policy leaves it empty (a job that declares its own key keeps it, so the field stays
      meaningful). `DedupeQueuedRuns` was deliberately **NOT** set: `maintenanceJobOpParams`
      carries `DryRun`, so "run for real" clicked during a dry run would be silently dropped —
      the exact bug #2688 fixed. Mutation-verified: with the key reverted, two enqueues overlap
      (`maxOverlap == 2`); with it, they run sequentially.

- [ ] **Still open from this fragment:** `LegacyOpID` (below) continues to defeat `EnqueueOp`'s
      byte-equality dedupe, because every request mints a fresh ULID. That is now the *only*
      remaining blocker to same-params merge for this family, and it disappears with
      `maintenance_dispatcher.go` in the v1 kill — so **re-measure after the v1 kill lands**
      rather than building a consolidator for it.

  **Where:** `internal/server/maintenance_job_op.go` — `registerMaintenanceJobOp` is the single
  factory for all 37 defs, so both fields are set in one place. `internal/maintenance/job.go:131`
  (`DefaultPolicy`) is where `ConcurrencyKey: ""` is hardcoded, and `job.go:123` explicitly defers
  per-job keys to "PR-2".

  **The state of things as originally found (fixed by #2709).** Two gates both test
  `def.ConcurrencyKey != ""`: `EnqueueOp`'s dedupe block (`registry.go`) and dispatcher Gate 3
  (`dispatcher.go:107`). Every maintenance job used `DefaultPolicy()`, whose `ConcurrencyKey` is
  `""`. So **neither gate had ever applied to a maintenance job**: a double-click started two runs,
  and they ran *concurrently*, not serialized. Both gates now apply.

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
