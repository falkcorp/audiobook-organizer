### Fixed

- **Series prune no longer strands books.** `executeSeriesPrune` (behind both
  `dedup.series-prune`, which the scheduler runs nightly, and
  `maintenance.series-prune`) decided a series was an orphan using
  `GetBooksBySeriesIDCore`, which skips trashed and non-primary books. A series
  whose books were all in the trash, or all secondary versions, counted 0 and
  was deleted while those books kept its `series_id`. On production 2026-08-14
  this had already produced **6,893 series IDs referenced by 13,322 live books
  (plus 702 trashed) with no series row** — those books render with no series at
  all. Orphan detection now uses a new unfiltered reference count
  (`GetAllSeriesBookRefCounts`) that counts every book naming a series whatever
  its deletion or primary-version state, computed once per run rather than per
  series. If the store cannot answer the unfiltered question the op now fails
  loudly instead of falling back to the filtered counter.
