### Fixed

- **The "Retention & Dead-Prefix Hygiene" maintenance job would have deleted the
  resume checkpoints of every current operation.** Long-running jobs — the iTunes
  import, the duplicate-drain, the file-hash backfill, the aggregate recompute —
  periodically save their position so that a restart picks up where they stopped
  instead of starting over. This job cleans up the positions belonging to
  operations that have finished, and to decide "has it finished?" it looked the
  operation up in the old operations storage.

  Nothing has been recorded in that storage since 2026-08-23. Looking an operation
  up there now returns nothing at all, and "nothing" was being read as "this
  operation is long gone, its saved position is garbage" — so the rule fired on
  every saved position in the database, including those of jobs that were still
  running. The job now checks the current operations storage first and falls back
  to the old one, and it keeps the position for anything that is not explicitly
  finished.

  **Nobody lost work to this.** The job is not on the maintenance schedule, it has
  to be started by hand, and it defaults to a preview run that deletes nothing. It
  had not been run for real since the change that broke it. Had it been, the
  affected jobs would not have failed or corrupted anything — they would have
  quietly restarted from the beginning on the next run, repeating hours of work.

  Worth knowing for the four "interrupted" states: three of them record a
  completion time even though the operation is still waiting to be resumed. The
  check deliberately does not treat a completion time as proof of being finished,
  because at this particular spot that would delete the position of an operation
  that is about to use it.
