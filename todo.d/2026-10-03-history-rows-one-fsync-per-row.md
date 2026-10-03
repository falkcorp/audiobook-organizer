- [ ] **HISTORY-BATCH-SYNC** Every metadata history row is its own synced
      Pebble write (`PebbleStore.RecordMetadataChange` → `p.db.Set(..., pebble.Sync)`),
      so `database.RecordBookEditHistory` costs one fsync per changed column.
      Since 2026-10-03 an operation revert records history for every column it
      restores (`internal/audiobooks/revert.go` `recordedModifyBook`), on top of
      the manual-edit and batch-edit paths. Estimate: a revert restoring 1–3
      columns on each of 1,000 books is 1,000–3,000 extra fsyncs, about 1–15 s
      at 1–5 ms per fsync on the app-data NVMe, serialized under the merge lock.
      A clean batch method was not added in PR #3703: it means a new method on
      `database.MetadataChangeStore`, which every store and mock must then
      implement. Done means: `RecordMetadataChanges([]*MetadataChangeRecord)`
      writes one book's rows in one `pebble.Batch` with a single sync,
      `RecordBookEditHistory` uses it, and a test proves partial failure
      records nothing.
