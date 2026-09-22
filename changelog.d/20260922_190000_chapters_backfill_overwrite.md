### Fixed

- `maintenance.chapters-backfill` gained an `overwrite` parameter, so a book with
  a *wrong* stored chapter timeline can be repaired. Until now the op skipped any
  book that already had chapters, and the ABS read path made the same assumption
  — both test only that the stored list is non-empty, so two junk rows were
  indistinguishable from a real 94-chapter timeline and the book played as one
  unnavigable block. `overwrite` is refused unless an explicit `bookIds` cohort
  is given, because a library-wide rewrite of every stored timeline is not a
  repair.
