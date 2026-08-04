<!-- file: todo.d/2026-08-04-dedupe-op-45s-per-book.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8333f42a-bd0d-4a2f-8221-403d11576e7c -->
<!-- last-edited: 2026-08-04 -->

- [ ] **PERF: `maintenance.dedupe-book-file-rows` spends ~45 seconds per book, and
      that is enough to blow its own 2-hour timeout.**

      Measured on the full production run (2026-08-04, op
      `01KZ6W1H46696CZDBHCZF10W6C`): 9 books in ~7 minutes, steady. Extrapolated over
      the 194 affected books that is **~2.4 hours against a `Timeout: 2 * time.Hour`**
      declared in `dedupeBookFileRowsDef()`, so the op cancels itself with roughly
      the last 40 books unprocessed and needs a second invocation to finish.

      Not a correctness problem — each book is committed independently and the op is
      idempotent, so a re-run simply picks up the remainder. But an op that cannot
      complete its own workload in one pass is mis-sized, and it will get worse, not
      better, as the library grows.

      **~45s to delete ~15 rows from one book is the anomaly worth explaining.** The
      per-book work is small: one `GetBookFiles` (Pebble-direct), a handful of
      `DeleteBookFile` calls, one `RecomputeBookAggregates`. Suspects, cheapest to
      check first:

      - `DeleteBookFile` → `notifyBookFileChange` may trigger a library-stats
        invalidation and full recompute **per row deleted**, not per book.
      - `RecomputeBookAggregates` re-reads the book's files; if it re-reads the whole
        library-level aggregate instead, that is the 5.6s full-scan class of bug
        already seen in `CountPrimaryBooks` (see
        [[project_countprimarybooks_cpu_fix]] — same shape, different caller).
      - The book loop is sequential. Per `CLAUDE.md`'s concurrency rule this is
        exactly a whole-library-scale loop doing meaningful per-item DB work, so it
        should have been a bounded `errgroup` pool from the start. Partition by book
        ID — books are disjoint, so parallel workers cannot touch the same row.

      Fixing the per-book cost is the real answer; raising the timeout only hides it.
