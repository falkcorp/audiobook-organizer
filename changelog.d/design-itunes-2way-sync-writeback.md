<!-- file: changelog.d/design-itunes-2way-sync-writeback.md -->
<!-- version: 0.1.0 -->
<!-- guid: 95c309af-6cdc-4fd4-aa66-a23c04c56987 -->
<!-- last-edited: 2026-07-22 -->

### Added

#### Design spec: iTunes 2-way sync writeback (edit-in-place, preserve play-state)

Added `docs/specs/2026-07-22-itunes-2way-sync-writeback-design.md`. An `itl-diff`
comparison showed the deployed `rebuild-full` writeback **regenerates** the library
(12,193 tracks / 14 playlists) versus the real library's 97,782 tracks / 356
playlists — valid but catastrophically lossy: no play counts, ratings, playback
bookmarks, music/podcasts, or user playlists. The spec replaces regenerate-from-DB
with **surgical edit-in-place**: base = the current library, relocate only audiobook
tracks via the existing `UpdateITLLocations` primitive (rewrites only the `0x0D`/`0x0B`
location mhods, preserving every other field), scope-gated by `IsAudiobook`, so
music/podcasts and all playlists survive verbatim. Documents the three op classes
(relocate / add / remove+ref-remap), the hard problems (match, topology + playlist
integrity, bookmark preservation), a sandbox-proven phasing (P0–P4), and open
decisions. Design only — no code, no prod actions.
