- [ ] **ACTIVITY-FLUSH-AFTER-CLOSE** `activity.Service`'s deferred-flush
      goroutine can write to the activity store after the store is closed,
      and pebble panics (`panic: pebble: closed`). Seen in CI on #3416
      (Coverage Floor, `internal/server`, around
      `server_startup_activity_test.go:39`). Stack: `startFlushLocked`
      (`internal/activity/service.go:176`) → `flushDeferred` (`:197`) →
      `writeDeferred` (`:215`) → `PebbleActivityStore.Record`
      (`pebble_activity_store.go:661`) → `Batch.Commit` on a closed DB. This
      is a shutdown-ordering bug in production code, not only a test flake:
      if the server closes the store while a flush is pending, the process
      can panic on shutdown and lose that batch. Done means the service stops
      and drains (or drops, with a log line) its deferred flush before the
      store closes, the server's shutdown order guarantees that, and a test
      closes the store with a flush pending and does not panic.
