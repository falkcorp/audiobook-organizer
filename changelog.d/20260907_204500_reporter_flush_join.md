### Fixed

- **A background goroutine could write to the operations store after it closed, producing a
  data race and a "pebble: closed" panic.** `dbReporter` started its log-flush loop as a
  fire-and-forget `go r.flushLoop(runCtx)`. Cancelling the run context only *asks* that loop
  to stop; on its way out it performs a terminal flush that writes to the database. Because
  nothing joined the goroutine, `executeRun` returned while that write was still in flight —
  the run left `Registry.running`, `Registry.Shutdown` concluded every worker had drained,
  and PebbleDB closed underneath the write. CI caught it as a `DATA RACE` inside Pebble's
  commit path, with the deferred `Batch.Close()` racing the goroutine doing the closing.

  `flushLogs` already had a `recover()` whose comment presented this as a test-teardown
  nuisance that turning the panic into a warning would handle. A `recover` cannot fix a data
  race: it catches the panic on one goroutine while the deferred close still races the
  closer. The comment now describes what the recover actually is — a backstop for the
  bounded-join path — instead of what it was believed to be.

  The flush loop is now a tracked goroutine (`sync.WaitGroup.Go`, per the org Go standards)
  and `executeRun` joins it before returning. The join is bounded at 10 seconds, because the
  terminal flush writes to Pebble and a compaction can block it — an unbounded join would let
  one stuck flush hang shutdown, which is worse than the bug being fixed.
