### Fixed

- **`VacuumActivity` now truncates the WAL, so the space it frees is actually returned.**
  In WAL mode `VACUUM` writes the rebuilt database *through* the WAL, and SQLite's automatic
  checkpoint is `PASSIVE` — it recycles the WAL in place at its high-water mark and never
  shrinks the file. Without an explicit `PRAGMA wal_checkpoint(TRUNCATE)` the freed bytes stay
  held by `-wal` indefinitely; no amount of waiting reclaims them.

  Caught on the first production run of the summary clamp (2026-09-08): the main database fell
  22,898,438,144 → 11,447,480,320 (10.66 GB freed) while `activity.sqlite-wal` sat at
  11,514,065,152 and did not move for three minutes afterwards. Net space returned to the
  filesystem was approximately zero.

  A vacuum that has committed is not failed by a checkpoint error — the error is reported so
  the caller can say the space is still held, rather than discarding a successful multi-GB
  rebuild.
