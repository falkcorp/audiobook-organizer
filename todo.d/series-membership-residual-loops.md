- [ ] **SERIES-MEMBERSHIP-RESIDUAL-LOOPS: two series loops still read membership per
      series.** Left out of the SERIES-MERGE-PERSERIES-SCAN-COST hoist (#2902) because
      the issue did not name them: `internal/plugins/maintenance/series_denumber_op.go`
      calls `GetBooksBySeriesIDAllVersions(pl.FromID)` per plan in both the dry-run
      preview loop and the apply loop, and `executeSeriesNormalizeCore`
      (`internal/server/duplicates_helpers.go`) calls it per action in its
      positions loop (plus the Core getter per action for the organize worklist).
      Same cost shape: a full `book:` Pebble scan per series once memdb is tainted.
      The fix is the same one: load `database.SeriesMembershipAllVersions` once and,
      for the denumber apply loop, `Move` each repointed book, because several plans
      can share a target.
