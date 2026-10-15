<!-- file: docs/executive-summaries/2026-10-15-boot-backfills-as-ops-executive-summary.md -->
<!-- version: 1.3.1 -->
<!-- guid: 9a4c7e12-6b3d-4f58-8e91-2d5f0b7c3a66 -->
<!-- last-edited: 2026-10-15 -->

# Startup library jobs now show up on the Operations page

**Pull request:** to be added when it opens (branch `task/04-p6`, task 04-P6, part 1 of 2; branch `task/04-p7`, task 04-P7, the book history index check).

## Executive Summary

- **Four startup jobs are now visible.** Every restart, the server ran four
  one-time library jobs in the background: linking iTunes IDs, cleaning
  movement tags out of audio files, repairing broken M4B files, and
  re-encoding and quarantining the ones that could not be repaired. They
  showed no progress and could not be stopped. They now run as ordinary
  operations, so they appear on the Operations page with a progress bar and
  a cancel button.
- **They wait for the library to finish loading.** After a restart the
  server spends a couple of minutes loading the library into memory. These
  jobs used to start while that was still running. They now wait until it
  finishes, or for at most five minutes.
- **A clean shutdown can stop all of them.** Two of the jobs, the iTunes ID
  linking and the quarantine step, ignored the operation's stop signal. A
  shutdown during one of them could keep it writing to the database while
  the database closed. Both now stop when told to.
- **Nothing changes when you run them by hand.** Running any of these jobs
  from the Operations page does exactly what it did before. Only the startup
  run passes the "skip if already done" settings.
- **Quiet when there is nothing to do.** On a library where these jobs have
  already finished, each startup run completes at once. The activity feed
  hides those runs by default.

## Still to do

Two more startup jobs get the same treatment in a follow-up: the book
path-set index build and the activity-log filter check.

## Owner decision (2026-10-15)

If the re-encode job is interrupted, it waits for someone to confirm before
it runs again. Startup now respects that: while an interrupted re-encode is
waiting, startup does not begin a new one. And once the re-encode and the
quarantine have both finished on a library, startup no longer queues the
re-encode at all, since it would have nothing to do.

## Stopping is now immediate

Stopping a repair or re-encode job now ends the file it is working on right
away, instead of waiting for that file's conversion to finish. An interrupted
file is left exactly as it was and is not marked as broken.

## A fifth startup job: the book history index check (04-P7)

Every restart, the server also checked a lookup table that lets a book's
change history load quickly. That check reads the whole history of changes,
which can take minutes, and it ran with no progress and no way to stop it. It
now runs as an operation too, on every restart, with a progress bar and a
cancel button. It has to run every time: if the server is ever rolled back to
an older version, that version adds history without updating the lookup
table, and only this check can notice. Until it finishes, book history still
loads correctly, only more slowly. If the check cannot make the table
trustworthy, the operation now shows as failed instead of only writing a
line to the server log. On a library where the table is already correct, the
run is hidden from the activity feed by default.
