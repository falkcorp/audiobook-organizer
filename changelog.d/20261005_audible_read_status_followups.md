### Fixed

#### `maintenance.audible-read-status` — review follow-ups

- **Every copy of the book counts.** The newer-local, local-progress,
  abandoned and manual-unstarted checks now read the listener's state on
  every live member of the target's version group, and on the book the ASIN
  or title actually hit, not only on the target. A listen on another copy
  newer than Audible's timestamp skips the finish. Progress on another copy
  skips the in-progress import.
- **Future timestamps are refused.** An Audible timestamp more than 10
  minutes in the future becomes `review_future_timestamp`, because it would
  beat every real listen that comes after it.
- **Zone-less timestamps are refused.** A timestamp with no zone is no longer
  read as UTC; it becomes `review_timestamp_unreadable`.
- **Contradictory exports go to review.** When the export's top-level
  `is_finished` and `listening_status.is_finished` disagree (the real export
  has such titles), the row is `review_audible_conflict` instead of being
  read as finished.
- **A status marked unstarted by hand is respected.** A manual `unstarted`
  status, on the target or another copy, is treated as the user's choice,
  like abandoned (`skipped_manual_unstarted`).
- **Listened seconds are never lowered.** The end position a finished import
  writes no longer lowers `TotalListenedSeconds`; the import keeps the larger
  of the two.
- **One lock for user-state writes.** The new striped
  `database.LockUserBookState(user, book)` lock is held across the
  read-check-write of a user's state and positions by:
  - the ABS write paths: state updates, PATCH and batch progress, session
    sync, offline replay, and progress reset;
  - the Repairs writer;
  - the revert of an import row.

  The writers that don't take it yet are listed on the lock itself:
  `readstatus`, the web reading heartbeat, the iTunes position sync and
  backfill, and merge follow.

A check of the real export confirms that `listening_status.finished_at_timestamp`
is the only listening timestamp. It is present on every finished title and on
about half of the in-progress ones. An in-progress title without it stays
`skipped_no_audible_timestamp`.
