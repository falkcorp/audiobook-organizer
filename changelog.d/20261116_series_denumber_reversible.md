### Changed

- The series de-numbering operation can now be undone. Every book it moves, every base series it creates and every numbered series it deletes is written to the operation's change log first, and reverting the operation puts the numbered series back under its own id with its books and positions as they were. An apply refuses to start if the run has no operation id or the store cannot restore a series.
- The operation no longer creates a base series for a plan that moves no book, and reports numbered series that hold no books at all as their own count (the empty-series cleanup removes them; this operation does not).
- A book is never moved onto a position another book already holds, never given a position over one it already has, and books from different numbered series that land on one position are all held back, each with a reason in the report.
- Names that read like a position but are not one are held out of the apply set with a named reason: a trailing "Part N", a format word in the base, a position of 100 or more, a base equal to any author's name, a base that still ends in a number, a base of one or two letters or a bare "chapter" word, a base that starts with a digit, and a series with no author.
- New parameters `minBooks`, `baseName` and a `limit` that counts only plans that move a book, so a first apply can start on one visible series. The report lists every book decision, and an applied run writes its outcomes next to it.

### Added

- Undo support for series rows: change types `series_create` and `series_row_delete`, and a store capability that recreates a deleted series under its original id.
- Reverting a de-numbering operation (or any operation that restored a series row or a book's series link) now refreshes the series lists at once instead of after the cache expires.
