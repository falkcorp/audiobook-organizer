### Added

#### `ops.Schedule` is now evaluated by the TaskScheduler, with a cron evaluator

A definition declares its timer once, in `ops.Schedule`, and the existing
TaskScheduler runs it on the clocks it already has: `Every` on the durable
interval clock, `DailyAt` on the daily clock, `InMaintenanceWindow` in the
nightly window order, and the new `Cron` on an in-tree five-field evaluator
(`internal/scheduler/cron.go`, no new dependency; DST gaps fire at the first
valid time, repeated hours fire once). The last fire, next fire, last run and
missed count of each schedule are kept in `opv3:sched:{def_id}`. A scheduled
writer runs as a Preview unless its schedule says `.Live()`; `ops.Schedule` gains
`.Preview()` for a job that is deliberately a Preview, and the catalog check
accepts exactly one of the two. A declared cron that nothing drives is listed as
"declared, not active" and never fires. A port keeps its cadence because the new
schedule is seeded from the old task's interval clock. Existing tasks are
unchanged; `internal/scheduler/testdata/cadence.golden` pins that.

The `audiobook_organizer.ops.schedule_lag` and `schedule_missed` OTel instruments
are now created and recorded.

Time a schedule's `When` gate was closed is not counted as missed fires,
including when the process restarts while the gate is closed (the closed state
is kept in `opv3:sched:{def_id}`); downtime while the gate was open still
counts. The gate is only known at the moments the process observed it, so a gate
change made while the process is down is not seen. Window definitions run before
`library_scan`, which stays last. A definition a legacy task still drives
reports that task's real next due time. The cron grammar has one parser
(`ops.ParseCronFields`) that rejects signed numbers such as `+5`, and a
scheduled writer is recognised by one predicate (`Effects.IsWriter`) in both the
scheduler and the catalog check.
