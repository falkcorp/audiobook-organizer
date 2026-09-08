### Added

- The activity log's move to its new storage now shows up as an operation you can
  watch, instead of running invisibly in the background. It reports which stage it
  is on, how many entries it has processed, and — if it does not finish — why,
  including after a restart. The count now advances while you watch rather than
  only when the page is reloaded. Previously the only sign it was running at all
  was in the server's raw log output, so a migration that had been working for
  hours and one that had quietly stopped looked exactly the same.

### Fixed

- Any long-running job that cannot report a percentage (because there is no
  meaningful total to count towards) showed "Starting…" for its entire run instead
  of the number of items it had processed.
