<!-- file: docs/specs/2026-07-23-itunes-2way-p0-findings.md -->
<!-- version: 1.1.0 -->
<!-- guid: 2c9e5a71-8b04-4d36-9f18-7a3c1e6b0d52 -->
<!-- last-edited: 2026-07-24 -->

# iTunes 2-Way-Sync — P0 Findings (read-only)

Executes P0 of `2026-07-23-itunes-2way-sync-system-design.md`. Verified against HEAD
`c5c311a6`. Read-only; nothing applied.

## F1 — K13 library-identity is NOT `LibraryPID`-only (RESOLVES §10b.2, the P1 blocker)

`guardLibraryIdentity` (`internal/itunes/itl_safety_contract.go:749`) checks **both**:
1. `LibraryPID` equality (via `ExtractLibraryPIDHex(hdr)` vs `ExpectedIdentity.LibraryPID`).
2. **track-PID sample overlap**: `ExpectedIdentity.SampleOverlapPct(after)` must be
   ≥ `IdentityMinOverlapPct` (**default 90**, `itl_safety_contract.go:1357`). The sidecar
   stores `PIDSample` = up to **1024** evenly-spaced track PIDs in payload order
   (`itl_identity.go:44,64`); overlap = how many still exist in the written library
   (`SampleOverlapPct`, `itl_identity.go`).

**Consequences for the design (§5 correction):**
- Normal iTunes activity is SAFE: adds don't remove sampled PIDs; play-state changes
  don't touch PIDs. Overlap stays ~100%.
- **Deletions/replacements of sampled tracks erode overlap.** At 90%, up to ~102 of the
  1024 sampled PIDs may vanish before K13 rejects.
- Therefore the steady-state count-auto-refresh (`RefreshLibraryTrackCount`, §5.3) MUST
  **re-derive the PID sample** from the current on-disk library each cycle (keeping
  `LibraryPID` pinned) — not merely refresh the count — or accumulated legitimate churn
  eventually false-rejects a valid relocate.
- **Drift-vs-reseed boundary is now concrete:** `LibraryPID` change = reseed (needs
  `adopt-base`); a large single-step sampled-PID loss = suspicious → the §5.2 drift-ceiling
  + §3.4 settle/quiescence gate catch it. K13 arming is well-defined; no blocker remains.

## F2 — Rebuild-caller audit CLEAN

`RebuildITLFromDB` and `ComputeITLDiff` are called ONLY from the two guarded handlers
(`internal/server/itl_rebuild.go:76` and `:171`); `rebuild.go:352` is the internal slog,
not a caller. Both handlers now carry `GuardRebuildTarget`. No unguarded rebuild path exists.

## F3 — `ProtectedPaths` is EMPTY on prod (safety gap to close)

Prod `/config` reports `protected_paths` empty/unset. So `books/itunes/**` is currently
NOT in the in-process protected set; the scanner avoids it only because it is not a
configured scan root (config-based, not a hard skip). **Action:** populate `ProtectedPaths`
with `books/itunes/**` on prod and adopt the config-load assertion (below) before any
`itunes.libraries`-driven op runs. The assertion is inert until `itunes.libraries` is
populated, so it does not affect the current deployment.

## Config scaffold landed (this PR)

`internal/config/itunes_libraries.go` — the 4-state model (`LibraryRef`/`LibrarySet` with
`PointedAt`/`ImportSource`), `ITunesConfig.Resolve()` (derives the legacy
`LibraryReadPath`/`LibraryWritePath` shims), and `ValidateLibraries()` (the four
fail-closed §2.4 assertions). Wired into `Config.Validate()` and viper load. **Inert until
`itunes.libraries` is populated** — empty `Libraries` → legacy behavior byte-for-byte.
Unit-tested (Resolve for both `import_source` values; all four assertions; back-compat).

## F4 — Cleanup provenance census: P3 is MEASURE-AND-STOP (provable removable set = 0)

**Measured 2026-07-24** against a consistent read-only copy of prod: ZFS snapshot of
`rpool/ROOT/ubuntu_0nm86n/var/lib` → the Pebble dir copied out of `.zfs/snapshot/` (13G,
1487 files, crash-consistent) + a copy of the live AO writeback `.itl`
(`.itunes-writeback/iTunes Library.itl`, 32,103,033 bytes). Ran
`pid-census --merge-provenance` (new mode, `internal/itunes/pid_integrity.go`
`ComputeMergeOrphanCensus`). All artifacts + the snapshot were destroyed afterward; the
`books@itunes-ao-fallback-2026-07-23` snapshot is intact.

```
tracks_in_itl        = 97999
  healthy            = 77211   (owned by a live PRIMARY book_file)
  stale_owner        =  7324   (owned by a soft-deleted / non-primary book_file)
  no_live_owner      = 13464   (NO book_file carries the PID — unattributable)
automerge_journal_entries = 0      ← the loser journal is EMPTY on prod
merged_into_losers        = 1
residual_duplicate_pids   = 3      (matches the 8,987→3 pid-repair; PIDs are unique)
>>> PROVABLE_MERGE_ORPHANS = 1   (sha_gated_removable = 0)
```

**Decision: P3 retires the unsafe `cleanup_merged.go` handler as a guarded no-op and
builds NO removal machinery.** The provable, SHA-gated, safely-removable orphan set is
**0** (1 candidate — "Stay of Execution", via `MergedIntoBookID` — with no live-primary
FileHash match, so not even SHA-confirmed). This is the measure-and-stop branch §6.5
names as the default expectation.

**Why the number is a floor, and why that STRENGTHENS the stop (advisor fork, confirmed by
code):** there is no durable, mutation-immune record of "what PIDs a loser owned at merge
time." `merge.Service.MergeBooks` (`internal/merge/service.go:228`) **reassigns** the
loser's `ext_id:itunes:*` mappings to the winner and soft-deletes the loser, but writes
**neither** the `AutoMergeJournalEntry` journal **nor** `MergedIntoBookID`. The journal is
written ONLY by `dedup/auto_resolve.go` (Tier-1 auto-resolve → 0 entries on prod);
`MergedIntoBookID` ONLY by `FlagMetadataHashDuplicate` (metafetch dedup +
`pebble_store.go:4030` → 1 on prod). And the PID-uniqueness repair already cleared
duplicate `book_file.ITunesPersistentID` off non-canonical rows. So a track that WAS a
loser's, whose PID→loser link was severed, lands in `no_live_owner` (unattributable), not
in the provable set. **`~0` here means "the provenance link is gone," not "no orphans
exist" — and you cannot build a provenance-anchored bulk remover from provenance that
isn't durably recorded.**

**The 7,324 + 13,464 "leftover-looking" tracks are NOT safely removable.** Samples confirm
`stale_owner` is dominated by legitimate version-group alternates
("Warbreaker … Part 1 of 2 (Dramatized Adaptation)", "Legion – Skin Deep") — the editions
§6.5 forbids removing (reject `version_group_id` basis). `no_live_owner` is unattributable
(user's direct non-audiobook imports OR severed orphans) and hands-off. Removing from
either bucket violates the spec's fail-closed rules.

**Two follow-ons (NOT this PR, NOT blocking the stop):**
1. **No durable merge-provenance trail on prod.** If a future release wants provenance-
   anchored cleanup, it must FIRST make `merge.Service.MergeBooks` record losers (write the
   journal at merge time), then re-run this census. Until then, cleanup is un-buildable
   safely. Also a latent gap for unmerge/audit recovery.
2. **`no_live_owner` composition** (13,464 = 13.7% of tracks): classify by audiobook genre
   to separate the user's non-AO music/podcasts (expected, hands-off) from severed
   audiobook orphans. Does not change the P3 decision (both are un-removable); informs any
   future re-attribution effort.

## Remaining P0 (not in this PR)

- **Cross-type PID collisions** (audiobook vs non-audiobook sharing a PID) — the
  disjointness-assertion backstop. Confirm PID-on-multiple-primaries stays 0 (post pid-repair).
- **Bookmark / field-preservation byte-proof** on a ZFS clone — run a relocate AND a
  track-remove through `SafeWriteITL`, then byte-compare every untouched track's record;
  assert ZERO changes (incl. the bookmark mhod). No preservation claim until it passes.
