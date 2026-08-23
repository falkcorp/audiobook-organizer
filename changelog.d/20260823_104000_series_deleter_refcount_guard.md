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

Review of the first cut found two ways the guard could pass on the very failure
it exists to catch, both now fixed:

- The refusal compared the unfiltered count against `len(books)` — the rows the
  merge loop *attempted* — rather than the rows it actually reassigned. A book
  whose `UpdateBook` failed still counted as moved, so a series with no hidden
  rows at all could be deleted while a book still pointed at it. Both merge
  paths now count successful reassignments only, and the previously-silent
  "listed but could not hydrate" case is reported instead of ignored.
- `getAllSeriesBookRefCountsPebble` never checked `iter.Error()` and skipped
  undecodable book rows, so a truncated or partially-corrupt scan returned a
  short map with a nil error. Because every guard reads a missing entry as
  "unreferenced", that undercount was fail-**open** at all three call sites at
  once. Both paths are now fatal.

Refusals are also no longer invisible: a group in which every merge was refused
no longer counts as an applied merge, phase-1 skips are counted, and both are
surfaced through the job reporter rather than only `slog`.
