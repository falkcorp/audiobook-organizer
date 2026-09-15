### Fixed

- Merge and duplicate resolutions no longer revert a concurrent writer's
  columns. Eleven dedup-path sites that read a book, changed one or two
  columns and wrote the whole row back (`UpdateBook`) now write through
  `ModifyBook`, which re-reads the row under the book's write lock and sets
  only the columns the site owns: the merge service's version-group demotion,
  absorbed-shell soft delete, combine title/narrator and author overrides and
  `SoftDeleteBook`; the combine undo's survivor-metadata restore; the
  `DedupSeries` and `MergeSeries` repoints; and the series prune, series
  group merge and stripped-position writes in the server's series
  maintenance. A row that already holds the target value is left unwritten.
  The combine undo's restore of an absorbed book stays a whole-row
  `UpdateBook` on purpose (it restores the journal's before-image). One
  lost-update test per package pins the fix (audit A1#15).
