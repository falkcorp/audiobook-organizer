### Fixed

- Nine maintenance jobs no longer revert fields another writer saved while
  they were running. Each read a book row, changed a field, and wrote the whole
  stale row back, so a metadata apply or edit that landed meanwhile was lost.
  They now write only their own columns through `ModifyBook`, under the book's
  write lock, and re-make their "still needs this" decision on the fresh row:
  - `normalize-primary-flags` sets only `IsPrimaryVersion`, and leaves a book
    that was grouped or flagged meanwhile for election.
  - `fix-author-narrator-swap` clears only `AuthorID`, and leaves a book whose
    author was already cleared or changed meanwhile alone.
  - `cleanup-series` repoints only `SeriesID` on merge and clears only
    `SeriesID`/`SeriesSequence` on unlink.
  - `relink-missing-to-itunes` writes only the new `FilePath`.
  - `purge-ua-duplicates` sets only the two soft-delete columns.
  - `backfill-metadata-source-hash` fills `MetadataSourceHash` only where the
    fresh row is still empty.
  - `refetch-missing-authors` sets `AuthorID` only where the fresh row still
    has none, so an author filled while the tags were being read is kept.
  - `fix-read-by-narrator` writes only `Title` and `Narrator`.
  - `revert-metadata-fetch` applies its per-field reverts to a fresh copy of
    the row instead of the copy it read before consulting the change history.
  Lost-update tests (`internal/maintenance/jobs/lost_update_test.go`) pin the
  normalize, swap and cleanup-series paths against a concurrent `Duration`
  write.
