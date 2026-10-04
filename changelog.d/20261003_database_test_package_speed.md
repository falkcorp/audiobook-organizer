### Changed

#### `internal/database` tests run on an in-memory Pebble filesystem

The package's `-short` run took 859 s on a Mac at 3% CPU: every Pebble write
in the package passes `pebble.Sync`, and each test opened its own on-disk
store, so the run spent nearly all of its time blocked in fsync. The shared
test helpers (`setupPebbleTestDB`, `setupTestDB`, the activity, embedding,
metrics and catalog store helpers) and the 121 inline
`NewPebbleStore(t.TempDir())` calls now open their store on `vfs.NewMem`
through `NewPebbleStoreInMemory`, which runs the same constructor, migrations
and write paths without the flush. Tests that reopen a database by path, or
whose window depends on real disk latency, stay on disk. The same run now
takes about 63 s. No assertion was removed and no test was gated behind
`-short`.
