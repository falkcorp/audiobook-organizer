### Fixed

- **A maintenance op that merged or deleted series left the series list showing
  the pre-op data for up to 24 hours.** The cached series list carries a 24-hour
  TTL and is warmed at startup, and until now only the interactive entities API
  invalidated it. Measured on production 2026-08-14: a `maintenance.series-prune`
  run reported *"17 duplicates merged, 326 orphans deleted, 0 errors"* and
  `/api/v1/series` kept returning all 14,629 rows with the same 329 zero-book
  entries — a completed repair that is indistinguishable from one that silently
  did nothing. `series-prune`, `series-normalize` and `series-denumber` now drop
  the cached list when, and only when, they actually changed rows; a run that
  cleaned nothing keeps the warm cache rather than forcing a full recount. Same
  defect and same fix shape as the authors-cache invalidation.
