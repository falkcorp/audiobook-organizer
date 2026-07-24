<!-- file: todo.d/itunes-2way-p0-cleanup-census.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8e2b5a41-6c93-4d07-9f18-3a1c7e6b0d52 -->
<!-- last-edited: 2026-07-24 -->

- [ ] **iTunes 2-way-sync P3 (cleanup) — decision: MEASURE-AND-STOP, no removal machinery.**
  The P0 cleanup provenance census ran on prod (97,999 `.itl` tracks): **provable merge
  orphans = 1, SHA-gated removable = 0** (`pid-census --merge-provenance`). P3 retires the
  unsafe `cleanup_merged.go` handler as a guarded no-op; do NOT build bulk removal. The
  count is a floor — prod has no durable merge-provenance trail (`merge.Service.MergeBooks`
  writes neither the `AutoMergeJournalEntry` journal nor `MergedIntoBookID`; the journal is
  empty). FOLLOW-ONS (not blocking): (1) if provenance-anchored cleanup is ever wanted, FIRST
  make the merge path record losers durably, THEN re-run this census; also a latent
  unmerge/audit gap. (2) Classify the 13,464 `no_live_owner` tracks by audiobook genre to
  separate the user's non-AO music/podcasts from severed orphans (doesn't change the P3
  decision). See `docs/specs/2026-07-23-itunes-2way-p0-findings.md` §F4.
- [ ] **iTunes 2-way-sync — remaining P0 measurements.** (a) Cross-type PID collisions
  (audiobook vs non-audiobook sharing a PID) — confirm PID-on-multiple-primaries stays 0
  post pid-repair. (b) Bookmark/field-preservation byte-proof: run a relocate AND a
  track-remove through `SafeWriteITL` on a ZFS clone, byte-compare every untouched track's
  record, assert ZERO changes. Then P1 (partitioned count-refresh, re-derive PID sample) /
  P2 (relocate-only sync-cycle op + oracle = MVP end).
