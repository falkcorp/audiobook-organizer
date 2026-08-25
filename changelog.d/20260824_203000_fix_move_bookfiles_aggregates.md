### Fixed

- **Moving files between books left BOTH books' runtime and size wrong, permanently.**
  `MoveBookFilesToBook` is the only store method that changes which book a `book_file`
  row belongs to, and it recomputed neither side. Duration and FileSize moved out of the
  source and into the target while both books kept their pre-move totals: the source went
  on counting runtime it no longer owned, and the target did not count what it had just
  gained. Nothing re-derived either figure afterwards —
  `maintenance.recompute-book-aggregates` is one-shot and refuses to run once its sentinel
  is set — so the wrong numbers were permanent rather than eventually correct.

  This is the merge path. All seven production callers are dedup/merge/regroup flows, so
  **every merge of two duplicate books left the survivor displaying a runtime that
  predated the merge.** The method now recomputes both books and refreshes the moved rows
  in memdb (which had kept the old owner, making the move invisible to the UI until the
  next warmup).

### Added

- `BatchCreateBookFiles` — creates many `book_file` rows in one atomic batch with a single
  coalesced aggregate recompute per book, and refuses a batch carrying the same iTunes
  persistent ID twice (the per-row uniqueness check reads committed state, so it cannot
  see a duplicate staged earlier in the same batch). The maintenance relink op's directory
  branch now uses it; production log attribution measured that one loop at 92.1% of all
  attributed aggregate recomputes.

- `MoveBookFilesToBookBulk` — moves rows from many source books into one target in a
  single atomic batch, recomputing each distinct book exactly once. Added because making
  the singular form recompute both books would otherwise have been a performance
  regression: three callers move files in a loop and two of them move one file per call,
  so a regroup covering 2,000 files would have paid 4,000 recomputes, each re-reading the
  target's entire and growing file set. `internal/merge/service.go`,
  `maintenance/itunes_regroup.go` and `maintenance/fs_regroup_xml.go` now use it. Both
  regroup paths fall back to per-file moves if the batch fails, preserving the resilience
  they had: a frozen plan can name a file that vanished before the apply, and one such
  file must not block every other move in its group.

### Changed

- Pinned `mockery` to **v3.7.4** (was v3.7.1) in `ci.yml`, `scripts/setup-mockery.sh` and
  the `Makefile` comments, and regenerated the affected mocks. The versions produced
  different output, so a locally-regenerated mock failed the `Mock Freshness` gate.
  Note that `ci.yml`'s own file header coincidentally reads `version: 3.7.1`; that is the
  workflow file's version, not the mockery pin.
