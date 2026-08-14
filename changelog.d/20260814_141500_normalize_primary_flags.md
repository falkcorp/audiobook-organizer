### Added

- New `normalize-primary-flags` maintenance job (dry-run default): writes
  explicit `is_primary_version=true` for ungrouped books whose flag is nil
  (effective-true by the memdb convention) or incoherently false, so every
  layer reads the flag the same way. Grouped books are counted but never
  written — primary-ness inside a version group is elected, not guessed.
