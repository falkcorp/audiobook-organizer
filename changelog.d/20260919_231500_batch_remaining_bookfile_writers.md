### Fixed

- `maintenance.mark-missing-files`, `maintenance.missing-file-repoint` and
  `maintenance.recover-missing-files` now write a book's rows in one batch
  instead of one row at a time. Each of the three walked a flat list of rows and
  per row re-read all of the owning book's rows (`GetBookFiles`) and recomputed
  the whole book's aggregates — two O(n²) legs in the book's file count. On a
  1,494-file book that is the same shape that made `duration-reextract` take ~25
  minutes on one book and get killed by the stuck-op watchdog. The work item is
  now a book: one read, one batched write, one aggregate recompute per book, and
  the worker pool's partitions are disjoint so two workers can no longer race
  each other's recompute of the same book. Liveness is still stamped on every
  row, and a lapsed scan stand-down lease still stops the write mid-book.
