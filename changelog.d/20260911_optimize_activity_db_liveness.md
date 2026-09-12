### Fixed

- `maintenance.optimize-activity-db` now declares `LivenessNone` with a 15-minute `ProgressTimeout`. It previously declared `LivenessManual`, never called `UpdateProgress`, and relied on a `reporter.Log` line, which does not reset the watchdog's progress clock. So the watchdog killed the one-time ANALYZE bootstrap at 5 minutes on every run (production, 2026-09-10 and 2026-09-11), and the activity store's query-planner statistics were never stored.
