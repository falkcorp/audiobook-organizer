### Fixed

- Interrupting activity-log compaction no longer inflates the summary counts it
  leaves behind. Compaction wrote a day's summary in one step and deleted that
  day's rows in a second one, and folded new counts into an existing summary
  unconditionally — so a run killed between the two steps would, on retry,
  re-count the rows that survived. A day of 5,001 entries interrupted after 5,000
  reported 10,001. Both the SQLite and Pebble backends had the same defect, and
  both now claim, count and delete each chunk of rows inside a single
  transaction, so the rows counted and the rows removed are the same set by
  construction.
- Compacting the activity log by hand no longer runs the server out of memory.
  Asking it to compact everything loaded all 13.2 million rows at once; it now
  works through them in bounded chunks.
- Pebble compaction no longer loses rows it cannot decode. They were deleted but
  left out of the summary count, so the total silently shrank.
- Nightly maintenance now survives a server that restarts more often than the
  job's own interval. Tasks scheduled every 24 hours tracked "when did I last
  run" in memory only, so on a host whose service lifetime averaged well under a
  day they could go a month firing twice. That state is now persisted.

### Added

- A nightly `maintenance.optimize-activity-db` operation that keeps the activity
  database's query-planner statistics current. It runs against whichever backend
  is active *and* the standby one, so switching backends cannot land on a
  database whose planner has never seen statistics.
