### Series table integrity — follow-ups from the 2026-08-14 prune repair

- [ ] **Identify what produced the 2026-08-11 burst of phantom series references.**
  Books reference 6,893 series IDs that have no row. The damage is EPISODIC, not a
  steady leak — only 10 distinct days ever, dated by ULID prefix on the book IDs:

  | day | books |
  |---|---|
  | 2026-06-18 | 78 |
  | 2026-06-19 | 16 |
  | 2026-07-19 | 507 |
  | **2026-08-11** | **5,367** |
  | 2026-08-12 | 133 |

  (the rest predate June; 7,220 landed in 2026-04.)

  The 08-11 books were all created within the same minute, 22:36 local, are loose
  files under `Unknown Author`, and carry titles like `Chapter 06` and
  `Singularity Online Book 3` — i.e. a scan of unsorted audio, not a maintenance
  op. Their series IDs are mid-range (153577, 165008, 165695), interleaved with
  live rows, NOT the contiguous all-dead 180000–184999 block.

  Checked and ruled out: no series-deleting op appears in the operations list for
  2026-08-11 or 08-12 (41 ops those days: purge-deleted ×18, temp-file-cleanup ×14,
  isbn-enrichment ×4, cleanup_activity_log ×2, maintenance-window ×2,
  metadata_candidate_fetch ×1). No scan op is recorded there either, which is
  itself worth explaining. `maintenance.series-prune` is ruled out for this burst.

  Two shapes to test: (a) the creating path assigns a `SeriesID` it never
  persists, or (b) it copies a `SeriesID` from another record whose series was
  already gone. Start from whatever created book `01KZSX7TW6BZXJX11F8K6Y0DSZ`.

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
