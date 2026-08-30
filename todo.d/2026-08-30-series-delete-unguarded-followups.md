- [ ] **SERIES-PHANTOM-REPAIR** Repair the series IDs that are ALREADY phantom.
      #2908 closed the last two paths that could create new ones
      (`dedup.MergeSeries` and phase 1 of `executeSeriesPrune` now consult the
      unfiltered `database.SeriesRefCounts` before deleting), but preventing
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
