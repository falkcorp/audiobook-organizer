### Fixed

#### Metadata is no longer applied while a library scan is running

- Every metadata-applying op (batch-apply-cached, batch-save, bulk metadata fetch, bulk write-back, maintenance and scheduler metadata refresh/upgrade, ISBN enrichment) now holds the scan stand-down before its first write and fails with a clear message if the scan does not park.
- Inline metadata writes over HTTP (single apply, fetch, write-back, batch update, bulk fetch, batch candidate apply) return 409 "a library scan is running; try again when it finishes" immediately instead of racing the scan.
- The organizer's auto-backup archive loop now has a stand-down checkpoint, so a scan in its backup phase parks between files instead of timing every stand-down holder out after 5 minutes.
