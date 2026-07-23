<!-- file: todo.d/itunes-2way-sync-continuation.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2165368b-70dd-48b0-b2d3-7288bbea666f -->
<!-- last-edited: 2026-07-23 -->

- [ ] **iTunes 2-way-sync — continuation (P3 redefine + reverse sync + footgun audit).**
  P1 relocate is applied+verified on prod (6,414). Still open, per
  `docs/plans/2026-07-23-itunes-2way-sync-continuation.md`: (1) redefine the P3
  merged-track removal to provable-duplicates-only (version_group/MergedIntoBookID
  linkage) — current `IsPrimaryVersion==false` criterion is UNSAFE (would delete real
  chapter files); explain the 4,298 shared-PID oddity. (2) Build the reverse sync
  (iTunes → writeback → AO) so media added/played/playlisted in iTunes syncs back once
  it's used full-time; decide the source-of-truth model + import from the writeback
  library not `books/itunes/`. (3) Guard/deprecate the destructive `/rebuild` +
  `/rebuild-full` against the now-real library; define the adopt-base steady-state.
  Dry-run + sample + owner sign-off before any destructive apply.
