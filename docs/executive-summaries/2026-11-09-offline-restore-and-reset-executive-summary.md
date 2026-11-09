<!-- file: docs/executive-summaries/2026-11-09-offline-restore-and-reset-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3022eed6-957a-4770-8cdf-e17ed51d8848 -->
<!-- last-edited: 2026-11-09 -->

# Restores and resets no longer touch the database while the server runs

**Date:** 9 November 2026

## Executive Summary

- Until now, restoring a backup from Settings unpacked it straight over the
  database the server was using. A crash or a bad backup partway through could
  leave a database that matched neither the old library nor the backup.
- Now a restore from Settings first unpacks the backup to one side and checks
  it. It must be a complete database this version can open, and with "Verify
  backup" on it must match its checksum. Only then is it lined up for the next
  restart, and the server restarts itself to apply it. The swap happens before
  the database opens, and the old database is moved aside, not deleted.
- A restore that cannot run is refused before anything changes, and the page
  says why: another restore or reset is already waiting, the checksum does not
  match, the backup has no checksum to verify, or the backup is gone.
- The server's reset and factory reset routes are gone, and Settings hides its
  reset control. In this release a reset runs only from the command line with
  the service stopped: `audiobook-organizer storage reset`. It too is lined up and applied
  at the next start, with the old database moved aside.
- `audiobook-organizer storage restore-offline` restores a backup or an
  upgrade safety copy with the service stopped, either in place or into an
  empty folder.
- Every command-line tool that opens the database now refuses while the
  server is running, while a restore or reset is waiting or half-applied, or
  before the server has ever created the database. An update is also refused
  while a restore or reset is waiting.

## Why it mattered

The old restore wrote over a live database. If anything went wrong in the
middle, there was no clean way back, and the damage could go unnoticed until
books or settings were missing. Lining the restore up and applying it before
the database opens means a restore either happens completely or not at all,
and the previous database is kept to go back to.

## What to know

- After a restore from Settings the server restarts by itself. A container
  started without a restart policy must be started again by hand.
- The reset button in Settings is hidden until a later release brings it back.
  Undoing a factory reset restores the database but not the library files or
  Open Library data it deleted. A reset leaves `config.yaml` beside the
  database as it was.
- The `pebble-inject-skip` tool is replaced by `audiobook-organizer
  transcode-skip <file>...`, run with the service stopped.
