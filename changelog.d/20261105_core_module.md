### Added

- Internal: the core module (`core/`) and its build guards. The nested Go
  module `github.com/falkcorp/mediacore` holds its first three packages
  (`buildid`, `storage/keys`, `archtest`); ABO's layering guard now runs on the
  shared `archtest` engine; new guards check that core imports nothing from the
  app, holds no app key names, keeps the one genproto exclude, and resolves
  every shared module to the version the root uses. Nothing user-visible
  changes.
