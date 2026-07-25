<!-- file: changelog.d/itunes-oracle-playlist-preservation.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6f2c9a83-7b64-4d50-8e18-3c7b5a0e1d75 -->
<!-- last-edited: 2026-07-25 -->

### Added

#### iTunes relocate oracle now asserts playlist preservation (smart-playlist rules)

`VerifyRelocateWrite` now checks that the entire **playlist-list section** (msdh type 2 —
static + smart playlists, names, membership, and SmartCriteria **rules**) is **byte-identical**
before vs after a relocate, in addition to the existing per-track raw-byte checks. The
relocate mutate only rewrites track location fields and cannot touch playlists **by
construction** (`shouldUpdateMhohLE` gates on the 0x0D/0x0B location type + a track PID in the
plan); this turns that guarantee into an enforced, auto-rollback-on-failure assertion — so a
user's newly-created smart playlists and rule edits can never be silently lost on a live
write. New `RelocateOracleVerdict.PlaylistsPreserved` (+ `playlist-changed` violation).

Verified on the real 357-playlist library: a 300-track relocate reports
`playlists_preserved=true`. Unit-tested (relocate preserves; a single tampered byte in the
playlist section is caught).
