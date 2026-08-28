<!-- file: changelog.d/20260827_234500_resume_watchdog_baseline.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6d2c706f-2e4d-4bb8-8c0e-f51376140e2d -->
<!-- last-edited: 2026-08-27 -->

### Fixed

- Resumed operations now start the watchdog from the new attempt rather than
  inheriting a stale progress timestamp and being immediately canceled.
