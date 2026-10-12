<!-- file: docs/executive-summaries/2026-10-11-operations-v3-keyspace-executive-summary.md -->
<!-- version: 1.1.0 -->
<!-- guid: 51035e40-ef55-4eed-89bd-0a07b809e5b2 -->
<!-- last-edited: 2026-10-11 -->

# Operations get a second, exact record of every run, and a rollback can no longer leave it wrong

Branch `task/05-pr4` (05-PR4 of the operations v3 plan), [PR #3983](https://github.com/falkcorp/audiobook-organizer/pull/3983). Brief: `docs/proposals/2026-10-holistic/tasks/05/05-PR4.md`.

## Executive Summary

- **A new, exact record of every operation run.** The app runs background operations (scans, metadata fetches, repairs). Each run has always been saved as one "version 2" row. Every run is now also saved in a new "version 3" format. That format keeps exact counts per state (queued, running, finished, failed and so on), a history of every state change, and the bookkeeping that the planned chunk-by-chunk runner will need. A one-time database upgrade (migration 65) builds these records from the existing rows. It changes no operation's ID and leaves every old row in place.
- **Both formats are saved together, so the previous version can be redeployed.** Every change to a run writes the old row and the new record in a single save. Either both land or neither does. The previous app version reads only the old rows, so it can be put back at any time during the transition.
- **A rollback can no longer leave the new records wrong.** While the previous version runs, it updates only the old rows. Rolling forward again does not re-run the one-time upgrade. Before this fix, a run that version created, finished or deleted kept a stale new record, or none at all, so the new counts were off with nothing to correct them. Now each new record stores a fingerprint of the exact old row it was last in step with. At every start-up, before operations resume, the app checks every run. It rebuilds each record whose row has changed, adds any that are missing, and removes records whose row is gone.
- **The start-up check is cheap when nothing changed.** On a store that is already in step it only reads: about 0.7 seconds for 100,000 runs, across all processor cores. It never touches the undo journal or any other per-operation history.
- **Progress updates no longer copy the whole record.** A running operation reports progress often. Each report used to rewrite the full new record, including the operation's parameters and result. Now the small, often-changing part (progress, phase, last checkpoint time and the fingerprint) lives in its own small entry. A progress report rewrites only that entry. In a test run with 6 KB of parameters and result, the new format's share of each update fell from about 6,800 bytes to about 380. Total bytes written per update roughly halved (13,709 down to 7,257). Speed per update is unchanged.
- **Stuck runs are no longer reported as finished.** When a run is stopped but its code keeps going past the grace period, it is now recorded as "stopping" until it really ends, rather than being marked finished while it is still running. Retry, the duplicate check and the watchdog all treat it as still active. After a crash, a run that was being stopped by a user cancel or a timeout keeps that outcome instead of starting over.
- **A record the start-up check could not fix is never acted on.** If the start-up check fails, the app still starts, because the old rows remain the source of truth. Now every new-format save first compares the record's fingerprint with the old row as it stands. If they differ, the save is refused and the old-format path decides instead. A stale record can no longer overwrite a newer row, bring back a deleted run, or delete a run that has moved on.
- **One damaged entry no longer stops the start-up check or the upgrade.** An entry that cannot be read is counted, logged and skipped, and the rest of the runs are still repaired. Before this, one bad entry could make the check fail at the same place on every start-up, or make the one-time upgrade block start-up entirely.
- **A stuck run that was stopped now ends as what it was.** A run that hit its time limit and kept going is recorded as "timed out" when it finally stops, not as "canceled". Canceling a run that is already being canceled no longer marks it finished early. A user's cancel that lands just as the app shuts down is kept, rather than letting the run restart at the next start-up. Retrying a different run of the same kind is no longer refused while a stuck run winds down.
- **Verified.** Tests cover the following:
  - A simulated rollback: the previous version finishes, stops, creates and deletes runs directly in the old rows, and the app restarts. Every affected record is repaired, and untouched runs stay byte-for-byte the same.
  - Every write path, followed by a check that the start-up pass finds nothing to do and writes nothing.
  - The check running while live updates hit the same runs.
  - A failed save that leaves both formats unchanged.
  - An upgrade that is interrupted part-way and then resumed: every run is built exactly once.
  - New-format saves refused against a changed or deleted old row, with nothing written.
  - A stuck run that hit its time limit, a cancel arriving during shutdown, and a retry while a stuck run winds down.

## Details

### A rollback left the new records stale

- **What it was.** The one-time upgrade runs once per database. The previous app version does not know about the new format, so anything it does after a rollback reaches only the old rows. When the current version comes back, the upgrade is already marked done and does not run again.
- **Why it mattered.** The new records feed exact operation counts. They also feed the start-up step that decides what to do with runs that were interrupted. A stale record could undercount finished work, or count a deleted run, until the next change to that run. A run that was never touched again stayed wrong for good.
- **The fix.** Each new record now carries a fingerprint (a hash) of the exact old row it reflects. Every save path stamps it: old-format writes, new-format writes and the one-time upgrade. At every start-up the app compares the fingerprints. A mismatch is rebuilt the same way the one-time upgrade would build it, and a record with no row is removed. The check runs before operations resume. Each repair rechecks under the same lock that live updates use, so it never overwrites a newer change.

### Progress updates rewrote large records

- **What it was.** Keeping the two formats in step meant that every progress or phase update rewrote the whole new record. That record includes the operation's parameters and result, which can be several kilobytes.
- **Why it mattered.** Progress updates are the most frequent operation write. Doubling their size adds up across long jobs, in disk writes and in database cleanup work.
- **The fix.** The record is split into a small, often-changing part and the rest, and each part is saved only when it actually changed. Reading a run joins the two parts, so nothing that reads runs sees a difference. A save of the old row and its small entry still happens as one unit, so a crash can never leave them out of step.
