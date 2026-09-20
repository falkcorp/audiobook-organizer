### Added

- **A report-only `maintenance.book-shape-report` op classifies the oversized-book
  and same-path split shapes and names a recommended treatment for each.** The
  2026-09-19 read-only investigation measured 205 books holding more than 200
  `book_file` rows (20.1% of every row in the library, in 0.27% of the books), 526
  two-member version groups sharing a path, and 2,409 directory paths carrying more
  than one book row — and found that no existing op covers any of it
  (`dedupe-book-file-rows` is within-book, `merge-same-path-dupes` excludes
  directory books by construction, `fs-regroup-xml`'s chapter-folder-layout apply is
  unimplemented, `probe-directory-books` only re-classifies). The new op walks every
  book and every `book_file` row, stats each grouping directory once, and emits one
  finding per shape: oversized books (threshold parameterised, default 200, with
  owned-rows vs files-on-disk, the path kind, `library_state` and
  `is_primary_version`); duplicate book rows at one path with their file-set overlap;
  the four split shapes distinguished by comparing member file lists (partition,
  containment, different-content, orphaned files); and corrupt paths (an absolute
  path concatenated onto another, and collision-suffix `- N` directory explosions).
  The shape ladder checks different-content *before* the merge shapes, so a record
  pointing outside its own folder can never be recommended for a merge. Output is a
  per-finding TSV under `{root}/.reports/`; the op declares `CapLibraryRead` only,
  has no apply mode, and its store interface carries no write method, so a library
  write cannot compile into it. The sweep shards path groups across
  `runtime.NumCPU()` workers and stamps liveness inside each shard, so a
  1,494-file directory on a slow mount cannot trip the stuck-op watchdog.
