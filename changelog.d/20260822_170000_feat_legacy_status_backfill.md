### Added

- A repair tool for old operations that are still shown as "pending" even though
  they finished. Operations created before August 16th never had their final
  status recorded, so the operations list has been reporting long-finished jobs
  as if they were still running. The new **Backfill Legacy Operation Status**
  operation works out what each one actually did — from the newer operation
  record that really ran the work — and corrects it.

  It reports what it *would* change by default and only writes when explicitly
  told to, so the plan can be reviewed first.

### Fixed

- The "stale operations" view could not see the stuck operations it was meant to
  surface. It ignored anything marked "pending", which is exactly the state the
  stuck rows are in, so it reported nothing was wrong while they sat there. The
  clear button used a different rule and *did* act on them — so the list you were
  shown and the rows the button touched disagreed.
