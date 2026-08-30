- [ ] **SERIES-PHANTOM-REPAIR** Repair the series IDs that are ALREADY phantom.
      #2908 closed the two paths it was filed against (`dedup.MergeSeries` and
      phase 1 of `executeSeriesPrune` now consult the unfiltered
      `database.SeriesRefCounts` before deleting). It did NOT close all of them —
      two more are filed below — but preventing
      corruption is not repairing it: the 6,893 phantom series IDs held by
      13,322 live books (+702 trashed) measured on production 2026-08-14 have no
      route back. Those books render with no series and nothing revisits them.
      Needs a report-first op that lists `books.series_id` values with no
      matching series row, grouped by how many books hold each, before deciding
      whether to null them out or recreate the missing series from the books'
      own metadata. Do NOT write a delete-first repair — see
      `docs/` and `internal/database/series_bookref.go` for why the filtered
      count is never the right existence test.

- [ ] **SERIES-NORMALIZE-TRASHED-GAP** `mergeSeriesGroupHelper`
      (`internal/server/duplicates_helpers.go`, used by the series-normalize op)
      is the third merge path and still has NO unfiltered reference guard. It is
      fail-CLOSED on everything it can see — an unhydratable row or a failed
      `UpdateBook` returns an error before `DeleteSeries` — so it cannot strand a
      row it was handed. What it cannot see is a TRASHED row: both series getters
      skip soft-deleted books, so a series whose books are all trashed enumerates
      empty and the row is deleted with those books still holding it. Left out of
      #2908 deliberately: the function returns a bare `error`, so surfacing a
      per-series refusal means either aborting the whole normalize run or changing
      the signature and every caller — a design call with its own blast radius,
      tests and mutation runs. Follow the `csMergeSeriesGroup` `(merged, refused,
      err)` shape when it is done.

- [ ] **SERIES-DENUMBER-TRASHED-GAP** `internal/plugins/maintenance/series_denumber_op.go`
      (~L328, op `maintenance.series-denumber`) is the FOURTH series-delete path
      and has the same trashed-row hole #2908 closed elsewhere. It enumerates
      with `GetBooksBySeriesIDAllVersions` and gates the delete on a `movedAll`
      flag that **starts true and is only ever set false inside the loop** — so a
      series whose books are all trashed enumerates empty, the loop body never
      runs, `movedAll` stays true, and `DeleteSeries(pl.FromID)` fires with those
      trashed rows still holding it. The file's own comment already documents
      that `movedAll` starts true; it closed the non-primary half by switching to
      `AllVersions` and left the trashed half. Not fixed in #2908 for a real
      reason, not an oversight: this op reaches its store through
      `p.deps.OpsStore()` (a `pkg/plugin/sdk` interface) and the package does not
      import `internal/database`, so calling `database.SeriesRefCounts` crosses a
      layering boundary — either widen the SDK surface or move the guard behind
      it. Found by review of #2983.
