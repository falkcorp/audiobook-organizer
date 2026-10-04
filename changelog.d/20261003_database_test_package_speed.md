### Changed

#### `internal/database` tests: in-memory Pebble and batched fixtures

The package's `-short` run took 859 s on a Mac at 3% CPU, and its `-race` run
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

After the change the package takes 35 s under `-short` and 161 s under
`-race`. Those runs had a load average of about 19 and 33-46, against 3-6 for
the before runs. Inside the full parallel `-short` suite the package went from
1,317 s to 340 s. CI runs on Linux, where fsync costs far less than macOS's
`F_FULLFSYNC`, so the gain there will be smaller than these Mac numbers. No
assertion was removed and no test was gated behind `-short`.
