### Fixed

- Eleven capability lookups resolved the store decorator by hand
  (`store.(interface{ Unwrap() database.Store })`) and unwrapped exactly one
  level. They now use `database.AsCapability`, which walks the whole chain. The
  production chain is one deep today, so this was correct by accident: a second
  decorator would have silently disabled query pushdown, the broken-file count,
  quick queries and transcribe stats — each degrading to a slower or empty path
  rather than failing. This is the same failure mode `store_capability.go` was
  written for after it reached production on 2026-07-30.

### Changed

- Those eleven sites no longer name `database.Store`. The anonymous-interface
  assertion was the last shape in the codebase that re-imposed the 398-method
  union without being visible to interface-shaped greps or to `interfacebloat`.
