### Fixed

#### Series deletes no longer strand books that the filtered count cannot see

Both series deleters decided "is this series still referenced?" from
`GetAllSeriesBookCounts` / `GetBooksBySeriesIDCore`. Those getters skip rows
that are `MarkedForDeletion` or non-primary — correct for a badge, wrong as an
existence test. A series with one primary book and three non-primary versions
reads as `count == 1`; the deleter unlinked the one visible book, deleted the
series row, and left the other three pointing at an ID that no longer exists.

This is the mechanism behind the damage already recorded in
`internal/database/series_bookref.go`: **6,893 phantom series IDs held by
13,322 live books plus 702 trashed ones**, measured 2026-08-14.

Fixed by giving both deleters the UNFILTERED count. A new exported helper
`database.SeriesRefCounts` resolves the capability through the decorator chain
(the live store is wrapped by the Bleve search-index decorator, so a bare type
assertion against `*PebbleStore` fails in production) and returns
`GetAllSeriesBookRefCounts`. Three call sites now refuse to delete when the
unfiltered count exceeds the rows they actually reassigned:

- `internal/dedup/series_dedup.go` — the refusal is recorded in
  `result.Errors` and the series row is kept. The check sits *before* the
  `dryRun` branch, so the preview and the apply make the same decision.
- `internal/maintenance/jobs/cleanup_series.go` phase 1 — a 1-book series whose
  unfiltered count is higher is skipped rather than collapsed.
- `internal/maintenance/jobs/cleanup_series.go` merge path — the merged-from
  series row is kept and a warning logged.

All three fail **closed**: a store that cannot answer the unfiltered question
aborts rather than falling back to the filtered count.
