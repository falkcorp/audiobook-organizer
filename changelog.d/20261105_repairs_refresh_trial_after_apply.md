### Fixed

- Repairs lane: rows you apply no longer stay listed ("sticky") until you run
  the trial again. A trial is a stored plan that is never edited, so an apply
  that wrote rows left them on screen. When an apply that wrote rows finishes,
  the server now plans that fixer again with the same settings (the same
  library read as "Run trial", read-only, low priority, one plan at a time),
  records it as the fixer's newest trial, and returns its operation id in the
  apply result as `replan_op_id`. The lane follows it, shows "Refreshing the
  trial after your apply" while it runs, and switches to the fresh rows when it
  completes. A preview, or an apply that wrote nothing, starts no refresh. If
  the refresh cannot be started the result carries `replan_error` and the lane
  tells you to run the trial again. Back-to-back applies each get a refresh
  that starts after their own writes, and applies made while a refresh is still
  queued share it.
- Repairs lane: the refresh after an apply does not hold Apply off. It takes
  minutes on a large library, so the next row can be applied straight away
  (a row the refresh still lists that an apply already wrote is refused as
  changed since the trial). Only a trial you started yourself keeps Apply
  disabled while it runs. You stay on your page of the list when the refresh
  lands (moved back only if the shorter list ends before it), and rows you
  ticked meanwhile stay ticked, less the ones the apply wrote.
- Repairs lane: a refresh that fails, or is dropped by a restart, no longer
  leaves the lane without rows. `GET /repairs` returns `last_completed_plan`,
  the newest plan that stored all its rows (`repairs_last_completed_plan_op:`
  setting), beside `last_plan`, and the lane shows it. Plan refs carry
  `after_apply` for plans the server started itself.
- Repairs lane: each apply's refresh retires the refresh it replaces. Their
  stored rows are deleted (only a refresh of the same fixer and settings that
  no running apply reads from; never a trial you started), so a day of applies
  does not pile up one full row set per click. A tab still showing a retired
  refresh and applying from it is told to run the trial again (409
  `REPAIR_PLAN_NEEDS_RERUN`).
