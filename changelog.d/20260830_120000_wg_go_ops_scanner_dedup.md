### Changed

- Converted 18 `sync.WaitGroup` `Add(1)`/`defer Done()` goroutine launches to
  `WaitGroup.Go` across `internal/operations`, `internal/scanner` and
  `internal/dedup`. Behaviour is unchanged — `Go` increments the counter
  synchronously before spawning, and its `Done` still runs after the body's own
  deferred cleanup, so semaphore-release and progress-channel ordering are
  preserved. Enrollments that deliberately `Add` under a mutex (the registry's
  `notifyStopped` gate and the dedup engine's `bgMu` gate) were left alone:
  moving those out of the lock would reintroduce an Add-after-Wait race.
