### Added

- Scheduler tasks can now run once a day at a fixed local time (`DailyAt: "HH:MM"` on the task definition). The next run is computed on the server's local calendar, so it stays at the same wall-clock time across daylight-saving changes. A run missed while the server was down fires once at the next startup and then goes back to the clock. The Maintenance tab shows "Daily at HH:MM (server time)" for these tasks. Tasks that repeat on an interval are unchanged.

### Changed

- `nightly_activity_compaction` now runs at 00:10 server-local time, just after midnight, so each run compacts the day that just ended. Before this it ran 24 hours after the first boot that saw it, at whatever time of day that was.
