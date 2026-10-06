### Fixed

- **Test servers no longer leak the SQLite activity checkpointer, and log
  captures are race-safe.** `TestResolveVectorBackend_HNSWIsSilent` failed
  the coverage job with a data race: a test server built by `NewServer`
  with `DatabasePath` set opened the SQLite activity store, whose WAL
  checkpointer goroutine only `Close` stops, and no test ever closed it. It
  kept ticking after the test's temp dir was gone and logged a WARN into an
  unguarded `bytes.Buffer` that a later test had installed as
  `slog.Default`.
  - New `newTestServer(t, store)` in `internal/server` tests registers a
    cleanup that cancels the server's background context, waits (bounded)
    for its goroutines, and closes the activity store. All 65 direct
    `NewServer` calls in the package's tests use it, and
    `setupTestServerFS` closes the store before removing its temp dir.
  - New `internal/logger/logtest` package: `Capture` (mutex-guarded text
    buffer) and `CaptureRecords` (mutex-guarded record handler). Both call
    `t.Setenv`, so a capturing test that is or becomes `t.Parallel` panics
    instead of silently sharing the process-global logger. Every
    `slog.SetDefault` capture in `internal/server` and its handler and
    middleware packages now goes through it.
