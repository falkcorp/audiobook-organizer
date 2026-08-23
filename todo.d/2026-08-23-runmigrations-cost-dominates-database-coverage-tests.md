- [ ] **DATABASE-RUNMIGRATIONS-TEST-COST** `internal/database`'s
      `-short` suite spends a large chunk of its wall-clock time re-running all
      60 `RunMigrations` migrations from a fresh Pebble store, once per test call.
      Found while profiling TASK-178 (`docs/agent-tasks/todo-completion`):
      `setupCoverageDB` (`store_coverage_test.go`, called 43x directly + 26x from
      `extra_coverage_test.go`) and `newTestStoreWithBook`
      (`book_file_test.go`, 17 callers) each call `NewPebbleStore` +
      `RunMigrations` per test. Measured `RunMigrations` on a fresh store at
      0.88s–2.4s (noisy) per call; a second call on an already-migrated store is
      ~29µs (the `len(pendingMigrations) == 0` short-circuit in
      `migrations.go`), confirming the cost is inside the 60 migrations' `Up()`
      bodies plus their `recordMigration`/`setVersion` writes (2 synced writes
      per migration in the loop — same fsync-per-item shape as the fix already
      applied to `memdb_warmup_writeloss_test.go`'s `seedBooksStore`), not in
      `NewPebbleStore` itself (~136ms/iter measured separately) or in the
      per-test CRUD bodies.
      Rough size: ~90 total `RunMigrations`-from-fresh calls across 7 test files
      (`book_file_test.go`, `external_id_map_test.go`, `do_not_import_test.go`,
      `metadata_history_test.go`, `quarantine_test.go`, `store_coverage_test.go`,
      `store_extra_test.go`) — closely matches `store_coverage_test.go` +
      `extra_coverage_test.go` + `book_file_test.go`'s combined ~96s of the
      package's ~323s -short top-level test time (measured before TASK-178's fix).
      Two directions to fix it, neither attempted here: (1) speed up
      `recordMigration`/`setVersion`'s per-migration synced writes in
      `migrations.go` — but that is production migration code, and durability
      guarantees there should not be weakened just to make tests faster without
      a real product-side review; or (2) build ONE shared, package-level
      migrated-store fixture (e.g. a `sync.Once`-created template directory,
      copied per test) that ~90 call sites across 7 files with 3+ different
      local helper signatures (`Store`-returning, `(Store, string, func())`-
      returning, and 8 inline call sites in `external_id_map_test.go`) would
      need to adopt — real effort, and each adoption needs its own correctness
      check that no test depends on a freshly-computed (vs. copied) migration
      side effect.
