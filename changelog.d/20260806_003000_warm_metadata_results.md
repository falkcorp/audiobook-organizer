<!-- file: changelog.d/20260806_003000_warm_metadata_results.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5c8107ea-3b92-4d67-81f0-6a4e29db3157 -->
<!-- last-edited: 2026-08-06 -->

### Fixed

- **The metadata-results set is now pre-warmed at startup**, so the first person
  to open the match UI after a restart no longer waits ~34 seconds for it to
  build. The build was memoised with a 60s TTL in #2142, but nothing populated
  the cache at boot — memoising moved the cost onto one unlucky request rather
  than removing it. Warming it removes it.

  Implemented as `warmMetadataResultsCache`, enrolled in the existing startup
  cache-warmer group alongside facets/authors/series: `bgWG`-tracked so shutdown
  drains it before the store closes, guarded on an already-cancelled `bgCtx`, and
  wrapped in `warmerRecover` so a warmup fault degrades to a cold cache instead of
  taking the server down. Best-effort by design — it never blocks startup.

  The TTL was deliberately left at 60s. Extending it to paper over the cold-boot
  gap would make every stale read staler; the right fix for "cold at boot" is to
  warm at boot.
