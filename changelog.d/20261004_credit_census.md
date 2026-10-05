### Added

- `maintenance.credit-census`, a read-only op that counts every way the flat
  author/narrator fields (`Book.AuthorID`, the `Book.Author` snapshot, the
  `Narrator` column, `NarratorsJSON`) disagree with the `book_authors` /
  `book_narrators` credit lists. It covers 22 classes, among them AuthorID with
  an empty join, a join with no AuthorID, AuthorID not primary or not in the
  join, unnormalized positions, combined-name records, a stale snapshot,
  narrator column/junction drift (including a stale junction behind a column
  that is not a list of people), NarratorsJSON drift, dangling or tombstoned
  ids, and books with no author. Each class stores its full sorted book id list
  in `GET /operations/:id/result`, so every count lists its books. It writes
  nothing. It is PR 0 of the author/narrator credit-list migration, and a
  re-run showing zero in every `disagreement` class gates the credits backfill.
