### Fixed

- Every operation-journal writer now records its rows through
  `database.RecordOperationChange`, which refuses a change type nobody has
  classified as user or library data. Before this, only `repairs.Writer` ran
  that check; the repoint, author strip/merge/relink, fs-regroup, series phantom,
  author merge, organizer, dedup, narrator and credits-backfill writers called
  `CreateOperationChange` directly, so a new type they wrote would have been
  served to every caller as library history. The classification table moved from
  `internal/undo` into `internal/database` (which `internal/merge` can reach),
  and `book_file_repoint` plus the organize, narrator, author and series-prune
  ledger types are now classified (all library data).

- The organizer and series-merge ledger writes that discarded their journal error now log a Warn (operation id, change type) when a row is refused or fails, instead of dropping it silently.

### Added

- An architecture guard (`internal/arch`) fails on any non-test file outside
  `internal/database` that selects `CreateOperationChange(s)`, apart from two
  named exceptions.
