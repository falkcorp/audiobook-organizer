<!-- file: changelog.d/20260803_210000_dedupe_book_file_rows.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8d51c72a-3f96-4b18-a0e4-27c60b9f5e31 -->
<!-- last-edited: 2026-08-03 -->

### Added

- `maintenance.dedupe-book-file-rows` — a dry-run-by-default op that removes
  duplicate `book_file` rows, where one book holds more than one row for the
  same `file_path`.

  Because a book's total duration and file size are a plain sum over its rows,
  duplication multiplies those totals by the duplication factor. Nine sampled
  books were affected at roughly 1.92×, including one showing 675.9 hours from
  66 rows covering 34 real files. This is distinct from the duration-unit
  problem: the units are correct, the rows are not.

  The op keeps the best-evidenced row — one carrying an AcoustID fingerprint
  wins over one with only a duration, which wins over one with only a file hash
  — and falls back to a stable id ordering so a dry run and the apply that
  follows it always choose the same keeper. Book aggregates are recomputed after
  each book's deletions. Pass `{"apply": true}` to delete; the default reports
  only.
