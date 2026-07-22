<!-- file: changelog.d/feat-itunes-relocate-writeback.md -->
<!-- version: 0.1.0 -->
<!-- guid: b3f0a1d2-8e64-4c97-a05b-1f7d2c9e4a63 -->
<!-- last-edited: 2026-07-22 -->

### Added

#### iTunes location-only relocate writeback (`/relocate`, `/adopt-base`)

New `POST /api/v1/itunes/relocate` repoints each iTunes-linked `book_file`'s track
at the file's current path, matching per-**file** `ITunesPersistentID` (the correct
granularity — one iTunes track per file). It emits **only** location patches: never
removes or adds a track, so music, podcasts, and all playlists are left byte-for-byte
intact — unlike `/rebuild`, which is DB-authoritative and would remove every track the
DB doesn't know (≈85,589 against a full library) and shatter playlists. `dry_run=true`
returns a preview (matched / to-relocate / already-correct / unmatched / unmappable).
New `POST /api/v1/itunes/adopt-base` re-blesses the `.identity.json` sidecar after the
writeback slot is reseeded from a different library (else the K13/K14 identity guards
reject every write).

Measured on production (ZFS-snapshot read-only DB scan): 85,783 of 85,788 unique
`book_file` PIDs (100.0%) match a track in the reseeded library, so relocate-by-PID
covers the whole audiobook set. Implements P1 of
`docs/specs/2026-07-22-itunes-2way-sync-writeback-design.md`. Also refactors the
book-level `canonicalWinLocation` to share the per-file canonicalizer.
