### Fixed

- **`POST /api/v1/activity/clamp-summaries` with `vacuum` now vacuums even when the pass
  clamped nothing.** The service skipped the vacuum unless `res.Clamped > 0`, which made the
  operation unable to fix the exact situation it was written for: once the first production
  clamp had already rewritten every oversized row, every later call found zero rows left,
  skipped the vacuum on that guard, and returned success while reclaiming nothing. There was
  no request that could reach the reclaim path.

  Concretely (2026-09-08): the clamp freed 10.66 GB inside the database and left
  11,514,065,152 bytes held by `activity.sqlite-wal`. Restarting the server did not release
  them either — SQLite deletes the `-wal` only on a clean last-connection close, and the
  `-shm` reset to 32,768 bytes across the deploy while the `-wal` did not move at all.

  `vacuum` is already an opt-in flag that defaults to false, so a caller who passes it has
  asked for it explicitly; conflating "explicitly requested" with "this pass changed
  something" is what created the gap. The flag is now honoured for any non-dry run.
