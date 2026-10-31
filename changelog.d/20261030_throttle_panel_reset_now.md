### Added

- System > Maintenance has a Provider Throttles panel. It lists the metadata
  providers currently on hold (reason, detail, time left), refreshes on mount,
  on demand and every 4 hours, and shows an empty state when nothing is held.
  Each row has Reset now, and there is a Reset all; both start
  `maintenance.throttle-reset` with `dry_run=false` through the ops-v3 trigger,
  so the run appears in the operations bell, and stay disabled until it settles.
  Buttons are disabled for callers without `scan.trigger`.
