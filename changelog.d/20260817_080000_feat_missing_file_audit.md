### Added

- **New `maintenance.missing-file-audit` operation** — finds the `book_file` rows
  behind "file not found" download failures. It stats every row's path and reports
  how many point at bytes that are no longer on disk, broken down per book
  (fully broken / partially broken / intact) and by library tree.

  Measured on the live library over a 120-book sample: 552 of 1,322 rows (41.8%)
  were missing and 49 of the 120 books had at least one dead file, 5 of them with
  no surviving file at all. Every missing path was under the organizer's own
  destination tree; nothing under the iTunes tree was missing.

  No existing operation could find these — `orphan-book-files-cleanup` matches rows
  whose `book_id` dangles, and `dedupe-book-file-rows` matches rows sharing an
  identical path, while these rows have a valid book and *different* paths.

  The operation is **report-only** and requests read capability only, so it is safe
  to run against a live library at any time, including during a scan.
