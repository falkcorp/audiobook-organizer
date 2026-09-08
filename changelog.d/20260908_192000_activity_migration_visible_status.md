### Added

- The activity log's move to its new storage now shows up as an operation you can
  watch, instead of running invisibly in the background. It reports which stage it
  is on, how many entries it has processed, and — if it does not finish — why,
  including after a restart. The count now advances while you watch rather than
  only when the page is reloaded. Previously the only sign it was running at all
  was in the server's raw log output, so a migration that had been working for
  hours and one that had quietly stopped looked exactly the same.

### Fixed

- The Activity page counted a whole day of finished jobs as active work — it read
  "Active Operations (91)" on a server where all 91 had already ended, and every
  one of them sat in a single "Completed" pile regardless of how it actually
  turned out. Finished jobs are now grouped by outcome — Completed, Failed,
  Canceled, Interrupted — newest first, with "Active" showing only what is
  genuinely still running. The page also stopped refreshing itself every five
  seconds while nothing was running.
- Jobs that ended by being interrupted (a restart, most often) were treated as
  still in progress everywhere they were displayed: they stayed in the running
  list with a frozen progress bar, inflated the notification badge, and were
  offered a Cancel button that could not do anything. There is a family of these
  statuses and only two of them had been accounted for.
- Any long-running job that cannot report a percentage (because there is no
  meaningful total to count towards) showed "Starting…" for its entire run instead
  of the number of items it had processed.
