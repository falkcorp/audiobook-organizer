<!-- file: docs/executive-summaries/2026-10-10-startup-step-classes-executive-summary.md -->
<!-- version: 1.2.0 -->
<!-- guid: ffeeeec4-a987-4932-b6d7-c01a304778f4 -->
<!-- last-edited: 2026-10-10 -->

# Startup: every step either stops the server or is reported

PR: [#3930](https://github.com/falkcorp/audiobook-organizer/pull/3930) (branch `task/07-r3`). Task brief:
[07-R3](../proposals/2026-10-holistic/tasks/07/07-R3.md).

## Executive Summary

- **One reviewed list.** Every step the server takes while starting up is now
  in one list that says whether a failure in that step stops the server, or
  lets it keep running in a reduced ("degraded") mode, and why. Before, the
  answer depended on how each step happened to be written.
- **A settings read failure no longer risks wiping your settings.** If the
  server could not read its saved settings, it used to start anyway on
  built-in defaults. The next time anyone saved a setting, the server wrote
  the whole settings record, so those defaults replaced everything that had
  been saved. It now refuses to start instead, and the same applies
  when a saved setting is damaged and cannot be read.
- **Monitoring trouble no longer takes the library down.** If the metrics
  system cannot start, the server keeps serving, and the health check says
  `metrics_disabled`. Before, the server would not start at all.
- **A missing search index is visible.** When the search index cannot be
  opened the server still runs without it, as before, but the health check
  now says `search_index` instead of saying nothing.
- **The list cannot quietly go stale.** An automated check fails if a startup
  step is added without an entry in the list, or if an entry is left behind
  for a step that no longer exists.

## Settings read failure

- **What it was:** a failure to read saved settings at startup was logged as a
  note and the server ran on its defaults.
- **Why it mattered:** saving any setting writes the complete settings record,
  so the first save from that server would have replaced the saved settings
  with defaults, silently.
- **The fix:** the read failure is now reported as an error and the server
  stops before it serves. An empty settings store (a fresh install) is not a
  failure and starts normally.
- **Background jobs too:** some jobs run in a separate helper process, which
  read the saved settings the same way and also carried on with defaults when
  the read failed. A job that saves settings (for example, duplicate-detection
  calibration) could have replaced the saved settings the same way. The helper
  process now stops with an error instead of running the job; a fresh install
  with nothing saved still runs normally.

## Damaged settings records

- **What it was:** when the server read its saved settings, any single setting
  it could not decode was quietly left out, and a database error part-way
  through the read was ignored. If the main saved configuration record was
  damaged, the server used older per-setting records instead, which on most
  installs means the built-in defaults.
- **Why it mattered:** this is the same wipe as above by a different route.
  The server ran on defaults, and the next settings save replaced the saved
  configuration with them, even though the damaged record could have been
  repaired.
- **The fix:** a damaged settings record, a damaged configuration record, or
  a database read error now stops the server (and the separate helper
  process) before it does anything. The error names the setting that could
  not be read, but never shows what is stored in it, since that can be a
  password or key. Repairing or removing the named record lets the server
  start. An older install that has never had a main configuration record
  still loads its per-setting records as before.

## Metrics start failure

- **What it was:** if the metrics system failed to start, the whole server
  refused to start.
- **Why it mattered:** a monitoring problem made the library unavailable.
- **The fix:** the server starts, logs the failure as an error, and the health
  check lists `metrics_disabled`.

## Search index failure

- **What it was:** the server already ran without search when the index failed
  to open, but nothing outside the log said so.
- **Why it mattered:** search silently worked differently, with nothing to
  notice from the outside.
- **The fix:** the health check lists `search_index` while the index is
  missing.
