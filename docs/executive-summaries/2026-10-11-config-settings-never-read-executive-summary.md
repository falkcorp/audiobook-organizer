<!-- file: docs/executive-summaries/2026-10-11-config-settings-never-read-executive-summary.md -->
<!-- version: 1.2.0 -->
<!-- guid: 3b9d7e52-1f6a-4c08-9e24-a7c5d1f0b863 -->
<!-- last-edited: 2026-10-11 -->

# Settings the server never read at startup, and old logs never cleaned up

Branch `fix/config-literal-missing-fields`. Follows the review of
[#3952](https://github.com/falkcorp/audiobook-organizer/pull/3952).

## Executive Summary

- **Old logs were never cleaned up.** The server has a weekly cleanup job
  for two kinds of old log entries:
  - the **system log**, which records what the server's background
    housekeeping did;
  - an **older style of operation log** that the server no longer writes.

  The job is meant to delete entries older than 90 days, but it never ran.
  The startup code skipped the "how many days of logs to keep" setting, so
  the server always saw 0, and 0 meant "keep logs forever". After this
  change the job runs and removes those entries once they are older than
  90 days.
- **Undo history is not touched.** The record of what each operation
  changed is what lets you undo it. The cleanup job never deletes it. An
  earlier draft of this change would have deleted undo history older than
  90 days along with the logs. It was caught in review and removed before
  it could ever run. The approved storage plan gives undo history its own
  setting, so no log setting can shorten how far back you can undo. That
  setting is a separate piece of work. It needs the owner's sign-off, and
  its first run will only count and report.
- **28 other settings were also ignored at startup.** All 29 skipped
  settings, including the log one, were silently dropped by the same
  startup code. A value put in the config file or an environment variable
  never took effect. They include:
  - the Deluge connection settings;
  - the AcoustID backfill switch;
  - the protected-paths list;
  - the activity-log retention windows;
  - plugin settings.

  The managed-tools section had the same problem in a different form: its
  seven settings were filled with fixed values that ignored the config
  file. A setting someone set only in the config file or environment is now
  honoured. Settings saved through the app were never affected.
- **List and map settings now work from every source.** Some settings are
  lists or tables, such as AI endpoints, plugins and protected paths. Set
  through an environment variable, they were silently thrown away.
  Protected paths written as `/a,/b` became one path that matched nothing,
  so neither folder was protected. Names inside these settings that use
  capital letters, such as a plugin called `MyPlugin`, were turned into
  lower case when read from the config file. All three problems are fixed.
- **This cannot quietly happen again.** An automated test now lists every
  setting the server has. It fails if startup does not read a setting, or
  reads it from the wrong place. It also fails if a default is set for a
  setting that does not exist. Nobody has to keep a list up to date by
  hand.
- **"0" no longer means "keep logs forever".** The 0 was almost always
  there by accident. However, from late August the scheduler's help text
  said "0 disables the purge", so someone may have chosen it on purpose.
  0 now means "use the default (90 days)". A stored 0 is removed once, on
  the first start after the upgrade, and the server writes a warning to
  its log. To keep logs forever, set the value to -1.
- **Saved settings still take priority.** A setting saved through the app
  still overrides the config file and the environment, as before.

## What changes on the production server

- **Only one thing changes: logs are kept for 90 days instead of
  forever.** We checked the server tonight:
  - none of the newly read settings is set in its environment;
  - the saved settings already hold a value for every one of them.

  So the saved values keep winning, and nothing else behaves differently.

## What to check after deploying

- The startup log should have one warning saying the stored
  `log_retention_days=0` was dropped. `GET /api/v1/config` should show
  `log_retention_days: 90`. If logs should be kept forever, set it to -1
  now, before the first cleanup runs.
- The first cleanup runs in the next maintenance window, or within a
  week. It has months of old entries to remove.
  - It deletes them in chunks of 5,000, saves its work after each chunk,
    and shows its progress as it goes.
  - If it is stopped, or runs out of its 30-minute budget, everything
    already deleted stays deleted, and the next run continues from there.
- **Disk use may go up briefly before it goes down.** Deleting records
  makes the database rewrite its files, and the old files are freed only
  when the database compacts them. On this server, ZFS snapshots keep
  holding the old blocks until the snapshots expire. Check that the
  data volume has room before the first cleanup runs.
