### Fixed

- **`maintenance.duration-reextract` no longer spends minutes on one large
  book, and it stops when cancelled.** Each drifted segment was written with
  `UpdateBookFile`, which recomputes the book's aggregates on every call, and
  that recompute re-reads all of the book's rows. On 2026-09-19 a 1,494-file
  book took ~25 minutes this way (one recompute per segment, ~1 s each), with
  no progress reported, and the stuck-op watchdog killed the run. The collector
  also ignored the cancel and kept writing after the op was abandoned. The fix
  adds a new store method, `UpdateBookFiles(ctx, files, afterRow)`. It writes
  rows by ID with `UpdateBookFile`'s semantics and recomputes each affected book
  once, not once per row. It checks `ctx` between rows. On cancel it still
  recomputes the books whose rows it wrote, and it returns recompute failures
  instead of only logging them. The op now writes each book's segments through
  it, stamps watchdog liveness after every row, and reports a throttled
  "segment i/n" progress line inside long books. It returns `ctx.Err()` on
  cancel, and its workers no longer block forever on the result send.
  `maintenance.purge-millisecond-durations` had the same per-row recompute and
  now uses the same method.
