<!-- file: changelog.d/docs-itunes-2way-sync-continuation.md -->
<!-- version: 1.0.0 -->
<!-- guid: 92609748-e3b2-4e21-8a54-ffc76f68f45b -->
<!-- last-edited: 2026-07-23 -->

### Changed

#### Doc: iTunes 2-way-sync continuation handoff

Added `docs/plans/2026-07-23-itunes-2way-sync-continuation.md` capturing the state
after the P1 relocate shipped and the remaining work: redefining the (currently
unsafe) P3 merged-track removal to provable-duplicates-only, building the reverse
iTunes→writeback→AO sync for full-time use, and guarding the destructive
`/rebuild` paths against the now-real library.
