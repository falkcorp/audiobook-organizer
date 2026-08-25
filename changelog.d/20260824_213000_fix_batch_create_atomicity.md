### Fixed

- **`BatchCreateBookFiles` was not atomic, despite its documentation saying so.** Taking an
  iTunes persistent ID from its previous owner went through a helper that commits its own
  write and recomputes its own book — writes the batch could not roll back. Combined with
  the duplicate-PID check added alongside it, that turned a rare timing window into a
  guaranteed one: the first row's transfer committed, the second row was rejected, the batch
  was discarded, and an **unrelated book was left with its ID stripped and pointing at a row
  that never came to exist.** The operation is now genuinely all-or-nothing.

- **A file row belonging to no book is now refused instead of silently orphaned.** It was
  previously written anyway, then invisible to every lookup, counted in no total, and logged
  nowhere at all.

- **A failed file-size read during relink repair is now logged.** It leaves that file
  recorded as zero bytes, and because a book's total only guards itself when *every* file is
  unreadable, a single readable file among many unreadable ones let a badly understated total
  overwrite the correct one — with no warning anywhere.

### Changed

- Removed a redundant recompute per transferred iTunes ID. Clearing an ID changes neither
  runtime nor size, so recomputing the previous owner's totals was pure waste — inside the
  method whose entire purpose is to avoid exactly that.

### Testing

- The relink repair test named `...AndAggregatesOnce` **never checked "once"**. Measured: it
  passes unchanged against an implementation that recomputes three times instead of one, so
  the regression it was named for could have landed under a green suite. It has been renamed
  to what it actually verifies, and the missing property is now pinned by a test that fails
  on that exact regression.

- Likewise, the duplicate-ID test could not observe the atomicity bug: its fixture had no
  previous owner, so the one code path that breaks atomicity was never entered, and its
  "nothing was written" assertion inspected the wrong book. Both gaps are now covered.
