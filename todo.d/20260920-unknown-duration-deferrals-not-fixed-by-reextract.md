- [ ] **`window-backfill`'s `unknown_duration` deferrals are NOT fixed by
      `maintenance.duration-reextract`, despite the comment saying so.**
      `internal/plugins/acoustid/window_backfill.go` (~line 398) calls
      unknown_duration "a retryable one (unknown_duration, fixed by
      maintenance.duration-reextract)". Measured on 2026-09-20 that is false for
      the population that actually defers:

      - before the reextract apply: T0 deferred 74%, unknown_duration 61%
      - after correcting 17,161 book durations: T0 deferred 2,661/2,673 (99%),
        unknown_duration 75%, written 12

      A follow-up run scoped to the residual
      (`onlyMissingDuration:true, force:true`) examined 8,252 books and returned
      **eligible=0, read-errors=8,227** — the zero-duration books are
      overwhelmingly UNREADABLE from the server, so no ffprobe-based op can give
      them a duration.

      Meanwhile the deferring T0 items are files the backfill itself just
      stat'ed successfully (it reads `fi.Size()`/`fi.ModTime()` when building
      `windowItem`), so the FILE is present — what is missing is
      `book_file.Duration` and `AcoustIDFingerprintDurationSec` on the row.
      `maintenance.duration-backfill` is not the answer either; it only repairs
      millisecond-valued durations.

      Two things to do: correct the comment so it stops sending operators at the
      wrong op, and decide what actually populates a per-FILE duration for a
      present file with an empty row — the remote worker already decodes these
      files and could report duration back, which would close the loop without
      any server-side I/O.
