### Changed

- `internal/metafetch` is now free of `database.Store`. The ISBN enrichment
  service takes four methods, the two free functions in `batch.go` take one
  each, and both `register.go` sites plus the `TryGet` in `lifecycle.go` assert
  what their constructor takes. `register.go` and `lifecycle.go` no longer
  import `internal/database`.
