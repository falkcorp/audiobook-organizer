<!-- file: changelog.d/itunes-2way-sync-apply-mode.md -->
<!-- version: 1.0.0 -->
<!-- guid: d71c94b2-0fce-427a-b3f6-7930c50da729 -->
<!-- last-edited: 2026-07-25 -->

### Added

#### iTunes 2-way-sync: `pid-census --sync-apply` (the P2 relocate COMMIT path)

`cmd/pid-census` gains `--sync-apply`, the committing counterpart to `--sync-dry-run`. It
runs `RunRelocateSyncCycle` with `Apply=true`, so the relocate-only sync cycle enforces the
quiescence gate, arms the `SafeWriteITL` contract (identity + magnitude + F7 scope +
pre-rename SHA re-verify), takes a `.bak`, runs the pre-commit oracle, writes only on a clean
verdict, then re-verifies post-commit and **auto-rolls-back from the `.bak`** on any
violation. The `--itl` path is the LIVE write target; `--db` is a copy of the Pebble DB.

`SyncCycleResult` now surfaces `PlaylistsPreserved` (from the oracle's playlist-list
byte-identity check, #2049) so the apply reports `playlists_preserved` explicitly; on Apply it
reflects the authoritative post-commit verdict.

First live use: a single drifted track in the production AO library was relocated
(`applied=true oracle_ok=true relocated_verified=1 playlists_preserved=true`, 358 playlists
preserved, 97,999 tracks unchanged); the post-apply dry-run confirmed the drift resolved
(`planned=0 already_correct=77,211`).
