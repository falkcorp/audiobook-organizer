<!-- file: changelog.d/20260806_010000_metadata_results_invalidation_race.md -->
<!-- version: 1.0.0 -->
<!-- guid: 0a3f7d29-64b1-4e08-95c7-812de6403bfa -->
<!-- last-edited: 2026-08-06 -->

### Fixed

- **Metadata-results cache: an invalidation could be silently undone by an
  in-flight rebuild.** Flagged by automated security review of #2153.

  The build runs outside the lock and takes ~30s. If a user applied or rejected a
  candidate during that window, `invalidateMetadataResultsCache()` cleared the
  entry — and then the in-flight build finished and wrote back the snapshot it
  had read *before* the invalidation. The cache ended up holding exactly the
  stale set the invalidation existed to purge, so the list kept offering a
  candidate the user had just acted on: the one kind of staleness this cache is
  explicitly not allowed to have.

  Fixed with a generation counter. A build captures the generation before
  reading; every invalidation bumps it; the build refuses to install its result
  if the generation moved, leaving the cache cold so the next read rebuilds from
  current state.

  The race existed in the pre-#2153 code too (the build was always outside the
  lock), but background refreshes were rare there — only on a cold miss. #2153
  made them routine, which turned a narrow window into a regularly-hit one.
