- [ ] **`PUT /tasks/:name` accepts `enabled` for tasks that ignore it, and still
      answers 200 "task config updated".** `UpdateTaskConfig`
      (`internal/server/handlers/operations/handler.go:503`) binds `Enabled`,
      `IntervalMinutes`, `RunOnStartup` and `RunInMaintenanceWindow`, then
      switches on the task name — but several cases wire up only a subset.
      `purge_deleted` (line 591) handles `run_in_maintenance_window` and has an
      empty `interval_minutes` branch with a comment where the code should be;
      it has **no `Enabled` branch at all**. `tombstone_cleanup`,
      `purge_old_logs`, `library_scan` and `library_organize` handle *only*
      `run_in_maintenance_window`.
      Measured 2026-08-16 against production: `PUT /tasks/purge_deleted
      {"enabled":false}` returned `200 {"message":"task config updated"}` and
      the task still read back `enabled=true`. The write was silently dropped.
      For `purge_deleted` the field is not even the right switch — `enabled` is
      derived from `PurgeSoftDeletedAfterDays > 0` (`internal/scheduler/tasks.go:614`),
      so the only real off switch is `purge_soft_deleted_after_days = 0` via
      `PUT /config`. An unsettable field should be rejected, not acknowledged:
      return 400 naming the fields that task actually accepts.
      Same shape as the iTunes backfill done-flag — a write-only field that
      reports success. Any "did it apply?" check must read back, never trust the
      200.
