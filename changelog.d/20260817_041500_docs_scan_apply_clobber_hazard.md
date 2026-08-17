### Fixed

- **Documented a silent data-loss hazard: applying metadata during a scan is
  reverted.** Nothing guards a metadata apply against an in-flight `library.scan`.
  For books the scan has not yet processed, `applyScannerFields` overwrites `Title`,
  `AuthorID`, `SeriesID`, `Narrator`, `Publisher`, `Language` and the provider IDs
  (`ASIN`, `OpenLibraryID`, `HardcoverID`, `GoogleBooksID`, `WorkID`) with
  scanner-derived values — and `Title` is effectively always overwritten because the
  scanner falls back to path extraction when tags are empty. The user sees no error.
  `preserveExistingFields`, which reads like the guard against exactly this, has a
  single call site confined to the moved-file branch. Filed with the mechanism and
  three candidate fixes; no behaviour change in this PR.
