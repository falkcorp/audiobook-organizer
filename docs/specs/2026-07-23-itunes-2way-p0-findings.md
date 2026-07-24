<!-- file: docs/specs/2026-07-23-itunes-2way-p0-findings.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2c9e5a71-8b04-4d36-9f18-7a3c1e6b0d52 -->
<!-- last-edited: 2026-07-23 -->

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

## Remaining P0 (not in this PR)

- **Cleanup provenance census** — enumerate the union of `MergedIntoBookID` +
  `AutoMergeJournalEntry.LoserID` (+ combine losers), resolve each loser's surviving
  primary, count the PROVABLE orphan set (loser PID in ITL, owned by no live book_file,
  survivor PID present, not a static-playlist member). Decides whether P3 builds or is a
  measure-and-stop no-op.
- **Cross-type PID collisions** (audiobook vs non-audiobook sharing a PID) — the
  disjointness-assertion backstop. Confirm PID-on-multiple-primaries stays 0 (post pid-repair).
- **Bookmark / field-preservation byte-proof** on a ZFS clone — run a relocate AND a
  track-remove through `SafeWriteITL`, then byte-compare every untouched track's record;
  assert ZERO changes (incl. the bookmark mhod). No preservation claim until it passes.
