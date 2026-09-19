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
- **The regroup track passes recompute the survivor once, not once per row.**
  `fs-regroup-xml`'s fragments track renumbering and the multidisc review
  apply (`applyDiscTrackNumbers`) wrote one `UpdateBookFile` per row.
  `UpdateBookFile` recomputes the book on every call, even for a
  TrackNumber-only change, so each row re-read all of the survivor's rows. Both
  now make one `UpdateBookFiles` call per survivor. fs-regroup journals exactly
  the rows that landed, stamps liveness per row, and skips its now-redundant
  trailing recompute. `UpdateBookFiles`' callback now reports the index of each
  row and whether it was applied.
- **A book_file write whose fsync failed now updates the book's totals.**
  `UpdateBookFile`, `ModifyBookFile` and `UpdateBookFileHashes` skipped the
  aggregate recompute on any error, including `ErrBookFileDurabilityUnknown`,
  where the row *was* applied and is visible. The book's totals then disagreed
  with its rows. They now recompute whenever the row was applied.
- **A disconnected browser no longer leaves a merged multidisc book
  half-numbered.** The disc/track numbering that follows a multidisc review
  approval ran on the request context, so a client disconnect or a proxy
  timeout after the merge had committed stopped it partway — and nothing
  finishes it later, because a re-approve finds fewer than two members and the
  numbering skips a survivor that already carries a number. It now runs to the
  end once the merge has committed. The fs-regroup fragments track pass is the
  same case: a group is applied atomically, so a cancel is honoured between
  groups, never inside one, where it could leave duplicate track numbers.
