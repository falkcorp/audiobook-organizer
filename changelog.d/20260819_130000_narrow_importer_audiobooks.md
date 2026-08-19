### Changed

- `internal/importer`'s `Store` is no longer `= database.Store`. It is seven
  measured methods plus four forwarding constraints embedded by name, and
  `CheckImportCollisions` takes a smaller slice still. `internal/audiobooks`'s
  two registry lookups assert its own constructors' types.
