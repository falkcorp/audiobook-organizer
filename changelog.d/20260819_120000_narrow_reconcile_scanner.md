### Changed

- `internal/reconcile` (4 methods) and `internal/scanner` (22) no longer depend
  on `database.Store`. `dedup.Store` is exported so callers that forward into
  `MergeBooks` and `MergeSplitBookCluster` can name the requirement.
