<!-- file: changelog.d/itunes-2way-p0-cross-type-census.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2f8b6c04-7d51-4e93-a1c6-9b3e5a0d1f72 -->
<!-- last-edited: 2026-07-24 -->

### Added

#### iTunes cross-type PID-collision census — relocate disjointness backstop (read-only)

`internal/itunes/cross_type.go` adds `ComputeCrossTypeCollisions`, and `cmd/pid-census` a
`--cross-type` mode. It classifies every track in the AO writeback `.itl`
(`isAudiobookITL`) and cross-tabs it against AO book_file ownership, surfacing the
load-bearing invariant for the relocate op: an AO book_file PID must resolve to an
audiobook track, never a music/podcast one. Bounded worker pool for owner resolution.

Measured on prod (97,999 tracks): the disjointness invariant **holds** — 0 real cross-type
collisions. The 3,436 tracks flagged are audiobooks that `isAudiobookITL` under-classifies
(genre histogram 100% book-shaped, zero music genres); AO's DB stores only audiobooks, so no
music/podcast track is AO-owned and a relocate can never make a cross-type write. Secondary:
`isAudiobookITL` misses `Audio Book`/`audio book` (space) and literary-genre audiobooks —
fail-safe for `GuardRebuildTarget` but not usable as a targeting filter. See
`docs/specs/2026-07-23-itunes-2way-p0-findings.md` §F5.
