<!-- file: changelog.d/20260810-memdb-sorted-indexes.md -->
<!-- version: 1.0.0 -->
<!-- guid: e93c17b0-5d84-4c26-a71f-08b6d3925fa4 -->
<!-- last-edited: 2026-08-09 -->

### Added

- **Optional sorted indexes for the library list.** Sorting by anything other
  than title currently disables pagination and materialises the entire
  filtered set — all 366,916 books — to return 50 of them. Nine sort fields
  (author, narrator, series, year, created_at, updated_at, duration,
  file_size, bitrate) can now be given a memdb sorted index, turning that into
  an ordered streaming walk the way title already works.

  **Off by default.** Each index costs real memory, and this was measured
  rather than estimated: at 100,000 books, enabling all nine took heap from
  2,645 to 6,395 bytes per book (+142%), which extrapolates to **+1,312 MB**
  at production scale, with inserts 2.8× slower. The design doc's estimate of
  "tens of MB per sort field" was optimistic by roughly an order of magnitude,
  because go-memdb's radix tree is immutable and path-copies nodes on every
  insert, so cost tracks node count rather than key length.

  With `enabled_sort_indexes` empty — the default — behaviour is exactly as
  before. Enable fields individually once you know which sorts are actually
  used, and re-measure warmup afterwards.
