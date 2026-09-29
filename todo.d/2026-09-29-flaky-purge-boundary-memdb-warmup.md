- [ ] **FLAKE-PURGEWARMUP** `TestPurge_SingleFileInSiblingKeepsParents`
      (`internal/audiobooks/purge_parent_cleanup_boundary_test.go`) failed on
      GitHub Minimal CI for #3622 (CI-only change, run 36609034263):
      `FilesDeleted = 0, want 1 ... cannot list every book_file row at a path:
      memdb is not serving`. `setupPurgeBoundary` opens a PebbleStore and
      purges straight away. The async memdb warmup had not yet published, and
      the purge's book_file lookup is fail-closed while it hasn't (correct for
      prod). Call `store.WaitForWarmup()` in `setupPurgeBoundary` and grep the
      other `internal/audiobooks` tests that purge right after
      `NewPebbleStore`. Done = 50 consecutive `-count=50 -race` passes.
