### Changed

- Nine `serviceregistry.Get[database.Store]` call sites now assert the interface
  their constructor actually takes. Five of those constructors were already
  narrow, so the wide assertion was purely vestigial. `internal/config` and
  `internal/backup` were narrowed to reach the same place.

### Fixed

- `internal/activity`'s registration resolved the concrete store with a bare
  `store.(*database.PebbleStore)`, which fails through the `indexedStore`
  decorator and silently takes the "non-Pebble backend" branch — disabling the
  Pebble activity store and falling back to NutsDB-only. It now uses
  `database.AsPebbleStore`.
