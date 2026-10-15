### Changed

- The hourly sweep of stale transcode temp files under each import path is now a visible scheduled operation, `maintenance.temp-file-cleanup` run from the new `transcode_temp_cleanup` task, instead of invisible goroutines started once at boot. Same rule (temp files older than 2 hours) and cadence, but every run now has an operation row, and an import path added while the server is running is swept without a restart. A missing import path is logged and skipped. The daily library-root sweep is unchanged.
