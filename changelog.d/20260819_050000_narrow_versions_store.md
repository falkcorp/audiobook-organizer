### Changed

- `internal/versions` no longer depends on `database.Store`. Its nine functions
  take measured slices — 12 methods across seven grouped interfaces, plus
  `database.ImportPathStore` embedded by name for the one forwarding constraint.
  Two of the nine (`scanFileHashMatch`, `ResumeVersionSwaps`) are stubs that
  never touch the store at all and now say so.
