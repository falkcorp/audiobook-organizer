### Added

- The activity log's move to its new storage now shows up as an operation you can
  watch, instead of running invisibly in the background. It reports which stage it
  is on, how many entries it has processed, and — if it does not finish — why,
  including after a restart. Previously the only sign it was running at all was in
  the server's raw log output, so a migration that had been working for hours and
  one that had quietly stopped looked exactly the same.
