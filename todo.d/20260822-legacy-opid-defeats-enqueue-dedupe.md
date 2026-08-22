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

- [x] ~~**Still open from this fragment:** `LegacyOpID` continues to defeat `EnqueueOp`'s
      byte-equality dedupe, because every request mints a fresh ULID.~~ — **fixed 2026-08-22
      (measured in PR #2717, fixed in the PR that closed this).**

      **The claim that it "disappears with `maintenance_dispatcher.go` in the v1 kill" was
      wrong, and re-measuring is what caught it.** `maintenanceJobOpParams` is constructed at
      three sites, and deleting the dispatcher removes only two — `server_lifecycle.go:287`
      (`resumeLegacyOp`) stamps a fresh `LegacyOpID` on the **restart** path with no dispatcher
      involvement, and `resumeInterruptedOperations` has no per-job dedupe, so
      restart-after-double-click reproduced the bug regardless. Repo-wide the field has ~30
      construction sites across nine subsystems; it is the v1↔v2 bridge seam, not a dispatcher
      artifact. The dedupe fix was therefore **independent of the v1 kill**, not gated on it.

      **The stamp was excluded from the comparison, not dropped.** Dropping it would have
      regressed two things: `propagateLegacyOpStatus` reads it to move the v1 row off
      `pending` (TODO.md records that bridge as measured working on 2026-08-16), and
      `maintenance_job_op.go:132,142-147` keys the activity log off it — the latter guarded on
      `p.LegacyOpID != ""`, so it would have failed **silently**. `Run` decodes from
      `rawParams`, not the `SaveParams` snapshot, so "keep it at :180" would not have helped.

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
