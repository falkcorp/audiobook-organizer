- [ ] **SERIES-DELETE-UNGUARDED** Two series-delete paths consult **no
      reference count at all**, so the unfiltered ref-count guard cannot
      protect them — a guard cannot help a call site that never asks it.

      - `internal/server/duplicates_helpers.go:213` — `executeSeriesPrune`
        **Phase 1**. `refCounter` is not constructed until ~L248, *after* Phase
        1 has finished. The loop enumerates via the FILTERED
        `GetBooksBySeriesIDCore`, appends reassignment failures to
        `mergeErrors`, and then calls `store.DeleteSeries(ser.ID)`
        **unconditionally — including after a reassignment it knows failed.**
      - `internal/dedup/series_dedup.go:642` — `MergeSeries`. Identical shape:
        filtered enumeration, errors appended to `result.Errors`, then an
        unconditional `DeleteSeries(mergeID)`.

      A series whose books are all trashed or all non-primary enumerates empty,
      reassigns nothing, and is deleted anyway. That is the original stranding
      bug (6,893 phantom series IDs held by 13,322 live books, measured
      2026-08-14) still live on these two paths.

      Confirmed fail-CLOSED and NOT affected: `cleanup_series.go:62`,
      `series_dedup.go:326` (`DedupSeries`), `duplicates_helpers.go:248-260`
      (Phase 2), and both `entities/handler.go` handlers (`:1009`, `:1043`).

      Fix: both should take `database.SeriesRefCounts` once before their loop
      and refuse to delete any series whose ref count exceeds what they
      actually reassigned — the pattern `csMergeSeriesGroup` already uses.

      Found by review of #2794, 2026-08-23. Pre-existing; outside that diff.
