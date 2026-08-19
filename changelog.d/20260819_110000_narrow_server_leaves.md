### Changed

- Thirteen leaf helpers in `internal/server`, `internal/server/handlers` and
  `internal/server/middleware` take measured store slices instead of
  `database.Store` — between one and eight methods each. `undo.ConflictChecker`
  and `versions.TrashedVersionCleaner` are exported so the server wrappers that
  forward into them can name the requirement.
