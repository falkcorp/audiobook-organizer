### Fixed

- **`maintenance.db-optimize` can now finish on a real database.** It never could.
  `store.Optimize()` is a single blocking `pebble.Compact(nil, 0xff)` with no
  callback, and the op reported progress only at its three step boundaries — so on
  any database where a full compaction takes longer than five minutes, the
  operations registry's stuck-detector cancelled it every time. Production is 31 GB:
  enqueued 10:25:45, `canceling stuck op ... no progress for 5m9s` at 10:30:54. The
  op is scheduled `0 2 * * 0`, so this had been failing silently every Sunday.

  Two details made it worse than a plain failure. The op declares `Timeout: 60m`, so
  the deadline its author chose never applied — a different, shorter clock fired
  first. And the cancellation stopped nothing: the op is `Cancellable: false` and
  `PebbleStore.Optimize` hardcodes `context.Background()`, so Pebble kept compacting
  for another twenty minutes while the registry reported the op dropped. An operation
  whose status says "dead" while its work runs on unsupervised is worse than either
  outcome alone.

  The compaction now reports from Pebble's own counters via a new
  `CompactionStats()` — completed compactions, and `EstimatedDebt`, the engine's
  estimate of bytes still to compact. Deliberately **not** a bare ticker: an
  unconditional heartbeat would satisfy the watchdog and destroy it in the same
  stroke, leaving a genuinely wedged compaction reporting healthy forever.
  `registry/types.go:200` already records that raising `ProgressTimeout` "hides the
  first case permanently", and a blind heartbeat is that mistake in a different
  costume. So progress is reported only when the counters actually move; if debt and
  completed-count are both flat while compactions are in progress, nothing is
  reported and the watchdog correctly kills it.

- **The main-store optimize log line no longer claims to run SQLite commands.** It
  said "Optimizing main database (VACUUM, ANALYZE, WAL checkpoint)..." — that is
  SQLite vocabulary, and the main store is Pebble. Nobody re-read the string when the
  engine changed underneath it.

- **A database compaction can now be interrupted.** `PebbleStore.Optimize()`,
  `AIScanStore.Optimize()` and `OLStore.Optimize()` all hardcoded
  `context.Background()`, so nothing could stop a running compaction — not an
  operation cancel, not shutdown. On a 31 GB store that is a twenty-minute window in
  which the process cannot be told to stop, and it is exactly what happened on
  production on 2026-09-08: the registry cancelled the op at its stuck strike and
  Pebble compacted on for another twenty minutes with the operation already reporting
  `interrupted_dropped`. All three now take a context and honour it, threaded from
  the op's own `Run(ctx, ...)` — which `runDBOptimize` had been discarding as
  `_ context.Context`. Aborting partway is safe: Pebble compaction is crash-safe and
  an interrupted run leaves the LSM as it was.
