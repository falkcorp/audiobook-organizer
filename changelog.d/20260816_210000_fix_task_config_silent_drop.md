### Fixed

#### `PUT /tasks/:name` no longer reports success for settings it drops

Turning a scheduled task off through the Maintenance settings page could do
nothing at all while reporting that it had worked. The endpoint answered
`200 {"message":"task config updated"}` for every field it was sent, including
fields the named task had no wiring for.

Measured against production on 2026-08-16: `PUT /tasks/purge_deleted
{"enabled":false}` returned 200, and the task still read back `enabled=true`
on the very next request. The purge kept running.

Two separate defects were behind it:

- **Five tasks silently ignored settings that do exist.** `library_scan` accepted
  and dropped `enabled`, `interval_minutes` and `run_on_startup` even though the
  scheduler reads all three from `scheduled.library_scan.*`; `reconcile_scan`
  dropped `interval_minutes` and `run_on_startup` the same way. Those settings
  now apply.
- **Fields that genuinely have no switch were acknowledged anyway.** For
  `purge_deleted` and `purge_old_logs` the on/off control is a different setting
  entirely (`purge_soft_deleted_after_days` and `log_retention_days`), and for
  `tombstone_cleanup` and `library_organize` the schedule is fixed in code.
  These now return an error naming the setting to change instead of a false
  confirmation, and the Maintenance page surfaces it.

The cause was that "which settings does this task accept" and "where does each
setting get written" were written as two separate things, so a task could accept
a setting it never wrote. They are now one table, and a test asserts for every
task and setting that a success response really did change the value it names.

A failure to save the change to the database also used to be logged as a warning
under a `200`; it now reports an error, since a setting that will not survive a
restart has not been updated.
