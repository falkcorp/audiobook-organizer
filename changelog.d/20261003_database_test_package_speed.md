### Changed

#### `internal/database` tests: in-memory Pebble and batched fixtures

On a Mac the package's `-short` run took 859 s at 3% CPU, and the `-race` run
took 992 s. Every Pebble write in the package passes `pebble.Sync`, and each
test opened its own on-disk store, so nearly all of that time was spent
blocked in fsync.

- The shared helpers (`setupPebbleTestDB`, `setupTestDB`, the activity,
  embedding, metrics, catalog and label helpers, `bookSigEnv`) and the 121
  inline `NewPebbleStore(t.TempDir())` calls now open their store on
  `vfs.NewMem`. They run the same constructor, migrations and write paths
  without the flush. `bookSigEnv` reopens on the same in-memory filesystem.
  Tests that reopen a real directory, or whose timing window depends on disk
  latency, stay on disk.
- SQL compaction and Summarize tests seed their fixtures through
  `RecordBatch`, which runs the same insert as `Record` in one commit. The
  multi-chunk SQL tests run at a 1,000-row chunk (`sqlActDeleteChunk` and
  `sqlActSampleWindow` become vars, like `sqlActSummarizeChunk`). The
  checkpointer tests still write row by row.

After the change: `-short` 35 s, `-race` 161 s, both on the same loaded Mac.
No assertion was removed, no test was gated behind `-short`, and statement
coverage went from 75.9% to 76.1%.
