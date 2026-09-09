### Activity compaction — three follow-ups found while fixing the double-count

Found during the chunk-atomic compaction rewrite. None of these are fixed by that
change; all three were verified against real production data or source, not inferred.

- [ ] **`RepairActivityIndexes` is time-unbounded and runs nightly inside the job
      that was just made survivable.** Memory is bounded, but it walks ~12M index
      entries doing a point lookup each, with no cutoff and no chunking. It is the
      remaining piece of `maintenance.cleanup-activity-log` that can still run
      until something kills it — which now means it can undo the benefit of the
      bounded compaction it runs alongside.
- [ ] **`maintenance.window` has no per-task panic isolation.** A nil-deref panic
      in task 1 of 10 (`dedup_refresh`) cancelled the other nine on seven
      consecutive nights, 2026-08-17 → 08-23. `cleanup_activity_log` is task 7,
      so activity compaction stopped running for a week and nothing reported it.
      One task's panic must not cancel unrelated maintenance.
- [ ] **Re-derive the "scheduled ops that never fire" list before acting on it.**
      The instrument used to produce it asked "does this def have a `scheduler.*`
      twin?" The correct predicate is "is this def a value in `taskV2DefIDs`?"
      (26 entries). At least 4 of the 27 are false positives with live
      `EnqueueOp` calls — `reconcile_scan`, `ai_dedup_batch`, `purge_old_logs`,
      `cleanup_activity_log` — mapped at `internal/scheduler/maintenance.go:174-178`
      and enqueued at `tasks.go:854/882/942/964/993`. `OperationDef.Schedule`
      really is decorative (written to `op_definitions_v2.ScheduleCron`, read by
      nothing, no cron library in the module), but that is a separate claim from
      "these 27 never ran", which is false as stated.

Related and worth doing regardless: a registry-level guard that rejects an
`OperationDef` declaring a `Schedule` nothing can execute. That would have caught
the decorative-field problem at startup instead of by archaeology.
