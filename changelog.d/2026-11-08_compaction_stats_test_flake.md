### Fixed

- Tests: `TestCompactionStats_RealPebble` no longer flakes with "a compaction must show written bytes". The test's Pebble instance now disables automatic compactions, so a background compaction cannot land before the `before` snapshot and zero out the measured delta; the explicit `db.Compact` is the only compaction and the test asserts it is counted and writes bytes.
