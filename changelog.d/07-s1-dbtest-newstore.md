### Added

- Test-only helper `dbtest.NewStore(t, opts...)` builds a warm, auto-closed Pebble store for tests, in memory by default, with `OnDisk()` and `WithoutMemDB()` options. It always waits for the memdb warmup before returning, so tests no longer race the warmup goroutine. No production behavior changes.
