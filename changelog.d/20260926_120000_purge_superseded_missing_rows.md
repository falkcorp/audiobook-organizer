### Added

- `maintenance.dedupe-book-file-rows` takes three new options, all preview
  by default (nothing is written without `"apply": true`):
  - `book_ids` limits the run to the listed books.
  - `cross_folder: true` deletes a book_file row whose file is missing on disk
    when exactly one present row in the same book has the same file name
    (case-sensitive) and the same size. Zero matches, two or more matches, a
    size mismatch, a row with no recorded size, and anything under
    `books/itunes/` are skipped with a reason. At apply time the op checks
    again with `os.Stat` that the row's file is still gone and the matching
    file still exists at that size. The deletion goes through the existing
    path: the kept row picks up any fields it was missing, the fingerprint
    windows move to it, the deleted row is written to the undo journal, and
    the book's duration and size are recomputed.
  - `remove_row_ids` removes the listed rows when another present row in the
    same book has an identical file hash, or when the call also passes
    `confirmed_duplicate: true`. Only the database row is removed; the file on
    disk is never touched, and a book is never left with no rows.
  The preview logs one line per row, with book id, row id, path, the matching
  row and its path, or the reason it was skipped. The same list is written to
  a `-decisions.tsv` report next to the per-book report.
