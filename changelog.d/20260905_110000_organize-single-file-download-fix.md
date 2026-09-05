### Fixed

- **Downloads and playback failed for single-file books after applying metadata.**
  Applying metadata re-organizes (renames) a book. For a single-file book the move
  updated the book record but left its `book_file` row pointing at the old path, so
  the download and streaming endpoints returned 404 ("bytes missing") for every
  single-file book an apply had renamed — even though the file was safe on disk at
  its new location. The organizer now repoints single-file rows on the move, the
  same way it already did for multi-file books. The stale row was also causing the
  next library scan to create a duplicate book record at the new path; keeping the
  row in step prevents that too.
- **`maintenance.missing-file-repoint` can now recover rows broken by that bug.**
  It gains a second derivation: when a missing row's owning single-file book has a
  real file at its own recorded path (matching size), the row is repointed there.
  Still report-only by default; still refuses any target that is ambiguous or
  already claimed by another row.
