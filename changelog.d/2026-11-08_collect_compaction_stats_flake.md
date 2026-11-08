### Fixed

- Tests: `TestCollectCompactionStats_RealPebble` no longer flakes with "a compaction must show written bytes" under load. Its Pebble instance now disables automatic compactions, so the explicit `db.Compact` is the only compaction and the test asserts it is counted, the same fix its core storage twin got in #4468.
