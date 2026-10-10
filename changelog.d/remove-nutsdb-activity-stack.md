### Removed

- Removed the retired NutsDB activity code (the NutsDB activity and metrics stores, the dual-write and instrumented wrappers, and the NutsDB-to-Pebble backfill) and the `nutsdb` dependency. Activity and metrics already run on Pebble; the tier list and filter helpers the Pebble store uses moved unchanged into `internal/database/activity_tiers.go`, and the tests that used the NutsDB store now run on the Pebble store.
