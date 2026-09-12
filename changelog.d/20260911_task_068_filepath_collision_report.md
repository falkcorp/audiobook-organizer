### Added

- **A report-only `maintenance.filepath-collision-report` op counts `Book.FilePath`
  collisions across the library.** TODO.md tracked 1,264 distinct `FilePath` values
  shared by more than one book (4,353 of 63,870 rows, 6.8%) and required the count
  be re-derivable before any future write path trusts `Book.FilePath` as an identity
  signal — most directly, before `missing-file-repoint` is extended to use it as a
  repoint source. The new op enumerates every book, buckets by exact `FilePath`
  match with a bounded worker pool sharding the reduce step across `runtime.NumCPU()`
  workers, and reports total books, distinct paths, collision-group and
  affected-row counts, and a capped sample of the colliding groups. It requests no
  write capability and never touches a book, book_file, or file on disk.
