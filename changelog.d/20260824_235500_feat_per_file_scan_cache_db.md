### Fixed

- **The scan cache is now keyed per file instead of per book** (database half of
  `docs/plans/2026-08-24-per-file-scan-cache-design.md`). It was consulted per *file*
  during the walk but built from *book* rows, so for a multi-file audiobook — one book
  row, many files — at most one file in N could ever be represented and the rest were
  re-read and re-hashed on **every** scan. Measured on production: *"436 of 500
  scan-cache write-backs skipped because no book row exists at the path"*.
  `GetScanCacheMap` now reads `book_file` rows, which fixes the value grain in the same
  move: each entry carries that file's own mtime and size rather than the containing
  directory inode's (128 bytes, observed). `UpdateScanCache` mirrors its stamp onto the
  file row for single-file books so the reader and writer stay at one grain, and a
  backfill can seed history so the first scan after deploy is not a whole-library
  cold re-read. The backfill also creates the one missing `book_file` row for
  single-file books that never had one — the scan never creates rows for those, so
  a file-keyed cache would otherwise have turned the population that caches
  *correctly today* into permanent misses. **The backfill is not yet wired to any
  caller** — it must be invoked once before the new reader is deployed, or the
  first scan re-reads the whole library.
