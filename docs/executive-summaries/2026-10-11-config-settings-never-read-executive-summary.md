<!-- file: docs/executive-summaries/2026-10-11-config-settings-never-read-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3b9d7e52-1f6a-4c08-9e24-a7c5d1f0b863 -->
<!-- last-edited: 2026-10-11 -->

# Settings the server never read at startup, and old logs never cleaned up

Branch `fix/config-literal-missing-fields`. Follows the review of
[#3952](https://github.com/falkcorp/audiobook-organizer/pull/3952).

## Executive Summary

- **Old logs were never cleaned up.** The server has a weekly job that
  deletes operation and activity log entries older than 90 days. It never
  ran. The startup code that loads settings skipped the "how many days of
  logs to keep" setting, so the server always saw 0, and 0 meant "keep
  logs forever". After this change the cleanup runs, and logs older than
  90 days are removed.
- **28 other settings were silently ignored at startup.** The same startup
  code skipped them too, so a value put in the config file or an
  environment variable never took effect. They include the Deluge
  connection settings, the AcoustID backfill switch, the protected-paths
  list, the activity-log retention windows and plugin settings. The
  managed-tools section had the same problem in a different form: it was
  filled with fixed values that ignored the config file. Most of them are normally set through saved
  settings, which were never affected, so in most cases nothing changed
  for users. A setting someone set only in the config file or environment
  is now honoured.
- **This cannot quietly happen again.** An automated test now lists every
  setting the server has and fails if startup does not read one. It also
  fails if a default is set for a setting that does not exist. Nobody has
  to remember to update a list by hand.
- **"0" no longer means "keep logs forever".** A 0 was what every install
  carried by accident, so it could not be told apart from a real choice.
  0 now means "use the default (90 days)". To keep logs forever, set the
  value to -1. A 0 that was saved by accident is removed once, on the
  first start after the upgrade, and the log says so.
- **Saved settings still take priority.** A setting saved through the app
  still overrides the config file and the environment, as before. In
  particular, the AcoustID backfill stays off unless it was turned on in
  saved settings.

## What to check after deploying

- The startup log should say the stored `log_retention_days=0` was
  dropped (once), and `GET /api/v1/config` should show
  `log_retention_days: 90`.
- The first log cleanup runs in the next maintenance window, or within a
  week. It deletes everything older than 90 days in one pass, so expect
  that first run to take longer than later ones. Splitting that first big
  delete into smaller pieces is filed as a follow-up.
