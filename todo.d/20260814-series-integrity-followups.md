### Series table integrity — follow-ups from the 2026-08-14 prune repair

- [ ] **Find what is still minting phantom series references.** Books reference 6,893
  series IDs that have no row; 5,500 of the referencing books were created in
  2026-08 and 507 in 2026-07 (88% of that month's books), so the source is live.
  `maintenance.series-prune` is ruled out as the *current* cause — it was fixed in
  #2400 and its two recorded runs never executed. Remaining suspects: the other
  series deleters that never got the unfiltered-reference guard, listed below.
  Dating method: ULID prefixes on the book IDs are time-sortable.

- [ ] **`BulkDeleteSeries` still deletes on a filtered count.**
  `internal/server/handlers/entities/handler.go:1017` guards with
  `GetBooksBySeriesIDCore`, the same display counter that skips trashed and
  non-primary books and caused the phantom references. It should use
  `database.AsSeriesBookRefStore(...).GetAllSeriesBookRefCounts()` like
  `executeSeriesPrune` now does. Same for the single-delete path at line 1007.

- [ ] **Two more series deleters have no cache invalidation and no ref guard**, and
  sit in packages with no path to the server's caches:
  `internal/dedup/series_dedup.go` (`DedupSeries`, `MergeSeries`) and
  `internal/maintenance/jobs/cleanup_series.go` (`csUnlinkAndDeleteSeries`,
  `csMergeSeriesGroup`). Consider moving invalidation into the store layer
  (`PebbleStore.DeleteSeries` already notifies memdb) so no caller can forget.

- [ ] **`WithOpID` is never called in production code**, so `ctxOpID(ctx)` returns ""
  for all 8 maintenance ops that read it (`series.go`, `cleanup.go` ×2,
  `write_back.go`, `reconcile.go`, `dedup_ops.go`, `optimize.go`, `metadata.go`).
  Every `CreateOperationChange` in `executeSeriesPrune` is therefore skipped: the
  2026-08-14 prune deleted 326 series and recorded zero changes, so there is no
  audit trail and no revert. `maintenance.purge-deleted` has the same gap while
  permanently destroying books. Note this also invalidates "0 changes recorded"
  as evidence that an op did not run.

- [ ] **~2,270 series look like they were created from a book title rather than a real
  series** (990 where the series name equals its only book's title, 1,280 where one
  contains the other). Do NOT delete on book-count alone: 2,322 single-book series
  are real series you own one book from (*Arliss Cutter*, *The Spiderwick
  Chronicles*, *Star Runners*). Needs a dry-run that emits the list, a hand-audit of
  ~40 of the "near" bucket, and its own apply gate — the repair must be narrower
  than the classifier.
