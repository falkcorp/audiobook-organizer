### Changed

- **`internal/server` tests run 6x faster (585s -> 98s).** Test-constructed PebbleDB
  stores now use an in-memory filesystem via the new
  `database.NewPebbleStoreInMemory`. Every Pebble write in this package passes
  `pebble.Sync`, so on a real filesystem each of the 60 migrations and every
  op-definition upsert cost a genuine `fsync` — a CPU profile showed only 90ms of
  CPU for a 1.6s test setup, with 94% of the time blocked in `os.(*File).Sync`.
  `setupTestServer` was called 275 times and 237 of the package's 909 tests sat in
  a single 1.50-1.75s band as a result. Production is unchanged: `NewPebbleStore`
  still opens the real filesystem, and the new constructor is test-only.
  Tests that need the database as real bytes on disk use `setupTestServerRealFS`.
