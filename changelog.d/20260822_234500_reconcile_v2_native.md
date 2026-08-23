### Fixed

- **The reconcile screen could stop showing your last scan.** Starting a reconcile
  scan handed back a tracking id that the operations endpoints no longer
  recognised, so the page had no reliable way to follow the run it had just
  started.

### Changed

- Reconcile scans and applies are now tracked entirely in the current operations
  system. Scans run before this change stay visible, with their previews intact —
  the "latest scan" view reads both the old and new records together.
