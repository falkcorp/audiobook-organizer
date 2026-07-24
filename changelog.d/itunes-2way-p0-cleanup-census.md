<!-- file: changelog.d/itunes-2way-p0-cleanup-census.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5a1c8e63-9d24-4f70-b3e8-2c6a1f9b0d47 -->
<!-- last-edited: 2026-07-24 -->

### Added

#### iTunes cleanup provenance census — P3 exit-gate (read-only)

`internal/itunes/pid_integrity.go` adds `ComputeMergeOrphanCensus`, and
`cmd/pid-census` a `--merge-provenance` mode. It buckets every track in the AO
writeback `.itl` by its current live owner (healthy / stale-owner / no-live-owner) and
intersects the stale-owner tracks with the merge-loser provenance set (the
`AutoMergeJournalEntry` journal reconciled with `MergedIntoBookID`), producing the
measure-first exit-gate the 2-way-sync design (§6.5) makes P3 conditional on. Bounded
worker pool for the per-owner book resolution. Read-only; a copy-of-prod tool.

Measured on prod (97,999 tracks): **provable merge orphans = 1, SHA-gated removable = 0**
→ **P3 is measure-and-stop** (retire the unsafe `cleanup_merged.go` handler as a guarded
no-op; build no removal machinery). The census also disproved the design's premise that
the auto-merge journal is the authoritative loser record — it is empty on prod, and the
production merge path records losers in neither durable source — which is exactly why the
count is a floor and bulk removal is un-buildable without new provenance recording. See
`docs/specs/2026-07-23-itunes-2way-p0-findings.md` §F4.
