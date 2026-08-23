- [ ] **`dedup.series-dedup`'s apply path writes no undo-ledger rows and does
      not check for a running scan.** TASK-043 gave the op a dry run
      (`dry_run` defaults to true), which covers the "read before you write"
      half of the destructive-op checklist. The other half is still missing:
      `DedupSeries` in `internal/dedup/series_dedup.go` calls `UpdateBook` and
      `DeleteSeries` without journaling either through `CreateOperationChange`,
      so a `dry_run=false` run is **not undoable via `internal/undo`** —
      `git revert` restores the code and nothing restores the data. It also
      does not refuse to start while a `library.scan` is running or queued, so
      a concurrent scan can clobber the reassignments. `MergeSeries` in the
      same file already threads an `opID` for exactly this purpose and is the
      pattern to copy. Do this before anything wires the op to a production
      trigger.
