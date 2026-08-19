### Changed

- `internal/plugins/dedup` (8 methods + 2 named constraints) and five holdout
  functions in `internal/plugins/maintenance` no longer take `database.Store`.
  The maintenance five compose interfaces that package already declared.
