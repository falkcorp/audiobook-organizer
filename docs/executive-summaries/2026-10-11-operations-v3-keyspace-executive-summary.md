<!-- file: docs/executive-summaries/2026-10-11-operations-v3-keyspace-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 51035e40-ef55-4eed-89bd-0a07b809e5b2 -->
<!-- last-edited: 2026-10-11 -->

# Operations get a second, exact record of every run, and a rollback can no longer leave it wrong

Branch `task/05-pr4` (05-PR4 of the operations v3 plan). Brief: `docs/proposals/2026-10-holistic/tasks/05/05-PR4.md`.

## Executive Summary

- **A new, exact record of every operation run.** The app runs background operations (scans, metadata fetches, repairs). Each run has always been saved as one "version 2" row. Every run is now also saved in a new "version 3" format. That format keeps exact counts per state (queued, running, finished, failed and so on), a history of every state change, and the bookkeeping that the planned chunk-by-chunk runner will need. A one-time database upgrade (migration 65) builds these records from the existing rows. It changes no operation's ID and leaves every old row in place.
- **Both formats are saved together, so the previous version can be redeployed.** Every change to a run writes the old row and the new record in a single save. Either both land or neither does. The previous app version reads only the old rows, so it can be put back at any time during the transition.
- **A rollback can no longer leave the new records wrong.** While the previous version runs, it updates only the old rows. Rolling forward again does not re-run the one-time upgrade. Before this fix, a run that version created, finished or deleted kept a stale new record, or none at all, so the new counts were off with nothing to correct them. Now each new record stores a fingerprint of the exact old row it was last in step with. At every start-up, before operations resume, the app checks every run. It rebuilds each record whose row has changed, adds any that are missing, and removes records whose row is gone.
- **The start-up check is cheap when nothing changed.** On a store that is already in step it only reads: about 0.7 seconds for 100,000 runs, across all processor cores. It never touches the undo journal or any other per-operation history.
- **Progress updates no longer copy the whole record.** A running operation reports progress often. Each report used to rewrite the full new record, including the operation's parameters and result. Now the small, often-changing part (progress, phase, last checkpoint time and the fingerprint) lives in its own small entry. A progress report rewrites only that entry. In a test run with 6 KB of parameters and result, the new format's share of each update fell from about 6,800 bytes to about 380. Total bytes written per update roughly halved (13,709 down to 7,257). Speed per update is unchanged.
- **Stuck runs are no longer reported as finished.** When a run is stopped but its code keeps going past the grace period, it is now recorded as "stopping" until it really ends, rather than being marked finished while it is still running. Retry, the duplicate check and the watchdog all treat it as still active. After a crash, a run that was being stopped by a user cancel or a timeout keeps that outcome instead of starting over.
- **Verified.** Tests cover the following:
  - A simulated rollback: the previous version finishes, stops, creates and deletes runs directly in the old rows, and the app restarts. Every affected record is repaired, and untouched runs stay byte-for-byte the same.
  - Every write path, followed by a check that the start-up pass finds nothing to do and writes nothing.
  - The check running while live updates hit the same runs.
  - A failed save that leaves both formats unchanged.

## Details

### A rollback left the new records stale

- **What it was.** The one-time upgrade runs once per database. The previous app version does not know about the new format, so anything it does after a rollback reaches only the old rows. When the current version comes back, the upgrade is already marked done and does not run again.
- **Why it mattered.** The new records feed exact operation counts. They also feed the start-up step that decides what to do with runs that were interrupted. A stale record could undercount finished work, or count a deleted run, until the next change to that run. A run that was never touched again stayed wrong for good.
- **The fix.** Each new record now carries a fingerprint (a hash) of the exact old row it reflects. Every save path stamps it: old-format writes, new-format writes and the one-time upgrade. At every start-up the app compares the fingerprints. A mismatch is rebuilt the same way the one-time upgrade would build it, and a record with no row is removed. The check runs before operations resume. Each repair rechecks under the same lock that live updates use, so it never overwrites a newer change.

### Progress updates rewrote large records

- **What it was.** Keeping the two formats in step meant that every progress or phase update rewrote the whole new record. That record includes the operation's parameters and result, which can be several kilobytes.
- **Why it mattered.** Progress updates are the most frequent operation write. Doubling their size adds up across long jobs, in disk writes and in database cleanup work.
- **The fix.** The record is split into a small, often-changing part and the rest, and each part is saved only when it actually changed. Reading a run joins the two parts, so nothing that reads runs sees a difference. A save of the old row and its small entry still happens as one unit, so a crash can never leave them out of step.
