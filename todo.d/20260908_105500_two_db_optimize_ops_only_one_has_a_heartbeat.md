## Two db-optimize ops exist; only one reports progress

`maintenance.db-optimize` (`internal/plugins/maintenance/db.go`) and
`scheduler.db-optimize` (`internal/scheduler/extra_ops.go:446`) are near-duplicates:
both compact the same three stores (main Pebble, AI scan, OpenLibrary), both walk the
same three progress steps, and both were registered — `plugins/maintenance/plugin.go:52`
and `server/scheduler_extra_ops.go:43`.

`fix/db-optimize-progress` gave the **maintenance** one a real progress heartbeat driven
by `CompactionStats()`, so the registry's five-minute stuck-detector no longer kills it
mid-compaction. The **scheduler** one still reports only at its three step boundaries, so
it has the identical bug: on a database where a full compaction exceeds 5m — production
is 31 GB — it gets cancelled every run. Its `Timeout: 2h` never applies, for the same
reason the maintenance op's `Timeout: 60m` never did.

Two questions, in this order:

1. **Should `scheduler.db-optimize` exist at all?** Two registered ops doing identical
   work with different timeouts and schedules is the actual defect. Deleting it is
   probably better than fixing it twice.
2. If it stays, share the heartbeat. `optimizeMainWithHeartbeat` is unexported in
   package `maintenance`; reuse means either exporting it or lifting a generic
   "run this blocking call, beat only when a caller-supplied probe says work happened"
   helper into `internal/operations/registry` next to `Reporter`.
