### Changed

- `internal/merge` no longer depends on `database.Store`. `Service`, `NewService`
  and the nine free functions now take measured slices — 18 methods across five
  grouped interfaces instead of 398. `collision.go` and `register.go` no longer
  import `internal/database` at all.
