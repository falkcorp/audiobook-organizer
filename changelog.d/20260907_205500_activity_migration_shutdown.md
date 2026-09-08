### Fixed

- **A restart during the Pebble→SQLite activity backfill could kill the process.** The
  backfill ran on a bare `go func()` started from `Start`, on `context.Background()`, and
  `sqlMigrationStarter` implemented no `Stop` at all — so the service container, which skips
  anything that is not a `Stopper`, had no way to reach it. The goroutine scans the entire
  activity keyspace on the *shared* main PebbleDB handle, and that scan runs for hours on a
  large library. A restart inside that window closed the store underneath it, and PebbleDB
  answers use-after-close with a panic rather than an error — on a goroutine with no
  `recover`, so it takes the process down mid-shutdown.

  The 60-second settle delay before the copy begins was a plain `time.Sleep`, which made it
  worse: a restart in the first minute after boot could not reach the goroutine at all, and
  shutdown proceeded while it was still sleeping toward a store that would be gone.

  The backfill now runs on a starter-owned cancellable context via `sync.WaitGroup.Go`, the
  settle delay is interruptible, and a new `Stop` cancels and then **waits** for the goroutine
  to return before shutdown can continue to close the store. The wait is unbounded on purpose:
  giving up after a timeout would reintroduce the exact use-after-close it exists to prevent.
  A cancelled backfill is reported as interrupted rather than failed — it is idempotent by
  content key and resumes on the next boot.
