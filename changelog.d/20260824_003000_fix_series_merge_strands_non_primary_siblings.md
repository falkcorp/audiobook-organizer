### Fixed

#### Three more series-merge paths no longer strand non-primary versions

A series merge repoints every book it is handed to the surviving series and then
deletes the merged-away one. Three paths read `GetBooksBySeriesIDCore` — the
**listing** getter, which hides non-primary versions — so an alternate rip of a
book was never repointed, and the series it still pointed at was deleted out from
under it. TASK-029 fixed this shape inside `internal/dedup/series_dedup.go`; these
are the paths outside it.

- `internal/server/duplicates_helpers.go` — two sites. `mergeSeriesGroupHelper`
  had **no guard at all**: its `DeleteSeries` is unconditional, so every hidden
  row was stranded. The `executeSeriesPrune` merge loop had the same shape.
- `internal/plugins/maintenance/series_denumber_op.go` — this one *looked*
  defended and was not. It refuses to delete unless `movedAll` is true, but
  `movedAll` starts `true` and is only ever set `false` inside the loop over the
  rows the filtered getter returned. A row that getter excluded is never
  iterated, so it can never flip the flag and the delete proceeds anyway. The
  guard's sample space was the filtered set the bug lives outside of.

`internal/maintenance/jobs/cleanup_series.go` was audited and deliberately **not**
changed here. It does not strand: its guard is driven by an independently-sourced
*unfiltered* count, so it refuses the delete instead. Its problem is the mirror
image — the refusal is permanent — and it was fixed separately.

> **Correction.** An earlier revision of this entry also described widening
> `executeSeriesNormalizeCore`'s affected-book list to the complete set. That
> change was wrong and has been reverted; see the follow-up entry for why. This
> paragraph is left as a correction rather than deleted, because the reasoning
> behind the mistake is the same one this whole entry is about.
