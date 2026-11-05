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
