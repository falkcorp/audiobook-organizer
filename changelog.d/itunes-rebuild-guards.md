<!-- file: changelog.d/itunes-rebuild-guards.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4c8e1b70-5d92-4a37-8f16-2b7a0c3e6d95 -->
<!-- last-edited: 2026-07-23 -->

### Fixed

#### `/rebuild` + `/rebuild-full` refuse to gut the real iTunes library

The DB-authoritative writebacks (`ComputeITLDiff` / `RebuildITLFromDB`) were designed
for a disposable, audiobook-only prototype library. Against the now-reseeded real
library (~98k tracks, ~86k music/podcasts, 357 playlists) they would mark every
non-audiobook / unmatched track for removal and shatter the playlists —
`/rebuild-full` especially, since it passes `ForceContractConfig()` which bypasses the
bounded-delta guard. Added an explicit, fail-closed target-shape precheck
(`GuardRebuildTarget`): the apply path refuses when the target library "looks real"
(non-audiobook track count over 1000, or more than 50 playlists), unless the caller
deliberately passes `allow_full_library=true`. Dry-runs are unaffected. This is
belt-and-suspenders over the existing K15 shrink-acknowledgement gate — it fails safe
even when the operation would not trip a >50% shrink.
