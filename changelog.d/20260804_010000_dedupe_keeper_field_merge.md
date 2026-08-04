<!-- file: changelog.d/20260804_010000_dedupe_keeper_field_merge.md -->
<!-- version: 1.0.0 -->
<!-- guid: ea73f5ef-cfab-4f6a-80c9-88fc14bacb1e -->
<!-- last-edited: 2026-08-04 -->

### Fixed

- `maintenance.dedupe-book-file-rows` no longer destroys data held only by the rows
  it deletes. The op collapses redundant `book_file` rows that all describe the same
  file on disk, and it chose which row to keep by ranking them. But ranking picks a
  whole **row**, and the best row is not guaranteed to be the most complete one: a
  row carrying an AcoustID fingerprint could have no duration at all, while the
  duration lived on a twin that was then deleted.

  The first canary run made this concrete. `The Trapped Mind Project` had 130 rows
  for one file. The op correctly kept the fingerprinted row — a fingerprint costs a
  full-file decode to regenerate — and correctly deleted the other 129, but that row
  had `Duration == 0`, so the book went to **0.00h**.

  The keeper now absorbs every field it is missing from the rows about to be
  destroyed: duration, AcoustID fingerprint, fingerprint duration, file hash, and
  file size. The merge is strictly additive — a value the keeper already holds always
  wins — so it can only recover data, never degrade it. The salvaged row is written
  **before** any twin is deleted, and a failed write skips the whole group rather
  than deleting donors whose data was never rescued.

  The canary that found this was run against 10 books (338 rows deleted) precisely
  so a design error would surface at that scale instead of across the whole library.

### Changed

- `maintenance.dedupe-book-file-rows` now states in its completion message that
  corrected totals may not be visible until the in-memory index refreshes. The same
  canary showed all 10 durations unchanged immediately after the apply, with a
  restart then revealing the already-correct values (`Defending the Lost`
  158.00h → 12.15h) — the data in PebbleDB was right the whole time and only the
  memdb-backed read was stale. The underlying cause is recorded in `todo.d/`:
  `RecomputeBookAggregates` early-returns without calling `UpdateBook` when the
  recomputed values match the stored ones, and `UpdateBook` is what triggers the
  memdb reload of `book_files`. Saying so beats letting an operator conclude the run
  did nothing.
