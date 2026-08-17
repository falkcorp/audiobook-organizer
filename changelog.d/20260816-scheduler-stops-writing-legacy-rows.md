### Changed

- The scheduler no longer writes rows to the legacy `operations` table. All 20
  remaining scheduled tasks and the maintenance window now enqueue a v2
  operation and report its id, so the id the scheduler logs and `/tasks/:name/run`
  returns is one that `GET /operations/v2/:id` can actually resolve.

### Fixed

- Scheduler: `isTaskRunning` covered 14 of the 24 tasks that enqueue an
  operation. The other 10 had no entry in its lookup table, and a missing entry
  is indistinguishable from "not running" — so the maintenance window would
  start a task that was already going, and the tasks page rendered it as
  stopped. Both guards now read the v2 record, and a test asserts the lookup
  against what each task actually enqueues, in both directions.

- Activity-log entries from scheduled maintenance ops were tagged with the id of
  a legacy row rather than the operation itself. They now carry the v2 operation
  id, which can be looked up.
