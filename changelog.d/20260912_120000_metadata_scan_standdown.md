### Fixed

#### Metadata is no longer applied while a library scan is running

- Every metadata-applying op (batch-apply-cached, batch-save, bulk metadata fetch, bulk write-back, maintenance and scheduler metadata refresh/upgrade, ISBN enrichment) now holds the scan stand-down before its first write and fails with a clear message if the scan does not park. The hold is renewed once per book, so a slow run keeps it for as long as it is making progress, and a lost hold stops the run before its next write.
- Inline metadata writes over HTTP (single apply, fetch, write-back, batch update, bulk fetch, batch candidate apply) return 409 "a library scan is running; try again when it finishes" immediately instead of racing the scan. The bulk handlers renew their hold per book, and the background file job of an apply keeps the hold until it finishes (or releases it if the job is dropped).
- A library scan picked up by a worker while a stand-down was held is now re-queued when the last holder releases, instead of staying parked until the next restart.
- The organizer's auto-backup now has a stand-down checkpoint in both the archive and checksum phases, so a scan in its backup phase parks instead of timing every stand-down holder out after 5 minutes; the partial archive is removed in either phase, and organize stops before fetching metadata.
