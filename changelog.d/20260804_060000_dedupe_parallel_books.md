<!-- file: changelog.d/20260804_060000_dedupe_parallel_books.md -->
<!-- version: 1.0.0 -->
<!-- guid: eef5897a-b0a5-43b8-bc12-79f2fc5a53bf -->
<!-- last-edited: 2026-08-04 -->

### Changed

- `maintenance.dedupe-book-file-rows` now processes books through a bounded worker
  pool (`registry.RunItems`, sized to `runtime.NumCPU()`) instead of one at a time.

  The book loop was a plain sequential `for range bookIDs` doing per-book database
  work — precisely the shape `CLAUDE.md`'s concurrency rule forbids, and the first
  full production run showed why: **~1.7 minutes per book**, so 176 books could not
  finish inside the op's own 2-hour timeout. Completing a 176-book cleanup meant
  three or four separate invocations.

  **Why parallelising is safe here.** Every unit of work is one book ID, and a
  `book_file` row belongs to exactly one book, so two workers can never touch the
  same row, the same keeper decision, or the same `RecomputeBookAggregates` target.
  This is the partition-into-disjoint-sets case the concurrency rule calls for, not
  a fan-out over shared state. The five counters and the examples slice are the only
  genuinely shared values and are mutex-guarded.

  `RunItems` also supersedes the intra-book heartbeat added a moment earlier: it
  stamps `UpdateProgress` as each book *completes*, via a monotonic counter that
  stays ordered even when books finish out of order — so the stuck-op watchdog is
  fed by completions rather than by a hand-rolled liveness ping. The 30-minute
  `ProgressTimeout` stays as defence for a single pathological book.

  A failing book no longer abandons the sweep: the callback returns `nil` after
  counting and logging, and `ErrModeCollect` governs cancellation. On cancellation
  or timeout everything already committed remains correct — books are independent
  and the op is idempotent — so a re-run picks up the remainder.

  Two new integration tests seed a real PebbleStore (24 books × 6 duplicate rows)
  and run the op end to end, so `go test -race` finally has a parallel path to
  inspect; the package's other tests are pure functions and exercised none of this.
  Verified the race detector actually catches a regression here: removing the mutex
  around a single counter produces `WARNING: DATA RACE` and a failure.
