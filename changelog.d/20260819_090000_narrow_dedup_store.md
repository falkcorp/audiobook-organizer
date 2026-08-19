### Changed

- `internal/dedup` no longer depends on `database.Store`. Its engine and entry
  points take 27 measured methods plus twelve forwarding constraints, all
  embedded by name. `merge.BookWriter` and `merge.UserProgressMerger` are
  exported so cross-package callers can name what they forward into.
