<!-- file: docs/executive-summaries/2026-11-09-offline-restore-and-reset-executive-summary.md -->
<!-- version: 1.0.2 -->
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
- The two commands refuse to run while the server is running or still
  starting, and the server refuses to start while either of them is running.
  Before this, starting the server in the middle of a restore could apply the
  same restore twice at once. The server also refuses to start while another
  program has the database open, such as a command-line tool or a second copy
  of the server, before it applies a waiting restore; before, it could move
  the database out from under that program.
- After a restore, the next start checks the duplicate finder's similarity
  index against the restored library instead of trusting the copy saved from
  the library it replaced. This closes the gap the duplicate finder's fix of
  8 November left for this work.
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
- If `storage restore-offline` is interrupted after it has lined up the
  restore, the next server start finishes it. If it is interrupted earlier,
  nothing has changed and it can simply be run again.
- The server, `storage restore-offline` and `storage reset` refuse to run
  when other users on the machine can write to the server's data folder (the
  state directory), because such a user could block restores or the server
  itself. The standard install's folder is fine. The refusal names the
  folder; `chmod g-w,o-w <folder>` fixes it.
- `storage restore-offline` (in place, or into an empty folder with
  `--into`), `storage reset`, `storage prune-checkpoints`, and any
  command-line tool that changes the database, run only as the user the
  server runs as, the owner of its data folder. Run as root, they would
  leave files behind that only root can read. The server would then fail
  to start. Or, after an `--into` copy or a prune, it would quietly stop
  clearing out old upgrade safety copies, and every later restore would
  wait 30 seconds and fail. An `--into` copy run as root before any safety
  copy existed would also have made the next upgrade fail. They now refuse
  before changing anything and print the command to use instead,
  `sudo -u <that user> audiobook-organizer ...`. Tools that only read the
  database are not held to this rule, though they still need the server's
  user or root to open it, and the two diagnostics tools that read it
  directly no longer write to its folder while they read.
- If a lock file from an earlier root run is still in the safety-copy
  folder, the server now logs it as an error, and a prune or restore
  refuses at once. Each names the file's owner and how to fix it, instead
  of the server skipping it in silence and restores failing after a
  30-second wait.
- The review asked for a stricter rule, a data folder only its owner can
  even read. The standard install creates the folder readable by everyone,
  and nothing tightens it, so that rule would stop the server from starting
  on every standard install. The tightening is filed as a follow-up.
