### Fixed

- **Corrected the recorded scope of the scan-error restore.** The TODO entry for
  "import-path scan no longer surfaces per-file scan errors" described the fix as
  frontend wiring — poll the operation, feed failures into `ScanStatus.errors`.
  That assumed the backend already collects per-file failures. It does not: nothing
  in `internal/scanner/` accumulates them, and the ones that are logged are free
  text at Debug/Warn with the path interpolated into the message rather than in
  structured `attrs`. The entry now describes both layers of work so the task is
  not picked up as a small one.
