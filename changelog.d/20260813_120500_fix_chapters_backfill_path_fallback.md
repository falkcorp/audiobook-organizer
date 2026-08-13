### Fixed

- **`maintenance.chapters-backfill` discarded ~16,000 recoverable books because its
  path fallback tested for an EMPTY path instead of a MISSING one.** Resolution fell
  back to `Book.FilePath` only when the `BookFile` row's path was the empty string. The
  common real failure is different: a move/organize updates `Book.FilePath` and leaves
  the `BookFile` row pointing at the old location, so the path is *populated and wrong*.
  Such a path sailed past the emptiness check and died 300ms later inside `ffprobe`,
  where it was tallied as a **probe failure** rather than the **resolution failure** it
  was — pointing at the audio files as the culprit instead of the database.

  Measured on the whole library: `probe-failed=16130`, 33.7% of single-file books. An
  independent `test -e` sweep over a 400-book random sample agreed (88 of 295 = 29.8%
  missing), ruling out `ffprobe` concurrency exhaustion. Of those 88, **86 (97.7%) had a
  `Book.FilePath` that was a regular file on disk** — recoverable, and being thrown away.
  Only 2 were genuinely gone.

  The fallback now fires when the `BookFile` path does not *resolve*, not only when it is
  *empty*. The `BookFile` row still wins whenever it resolves — it is the more specific
  source, and preferring the fallback unconditionally would reroute every book in the
  library rather than the broken ones. Non-regular files (a multi-file book's
  `Book.FilePath` is its *folder*) are rejected, since handing `ffprobe` a directory
  produces a confusing decode error instead of a clean skip.

  Books resolved through the fallback are counted and reported separately as
  `recovered-via-book-path=N`, never folded into the persisted total: those rows are
  written from a secondary path source, and if that source is ever wrong there has to be
  a way to identify the affected books after the fact.

  Checked before shipping: across all 63,870 book rows, 1,264 `Book.FilePath` values are
  shared by more than one book (4,353 rows) — but **0 of the 88 recoverable rows** were
  among them, so the fallback cannot currently write one book's chapters onto another.
