- [ ] **DEDUP-SERIES-MERGE-STRAND** Decide whether the three series-merge paths
      *outside* `internal/dedup/series_dedup.go` get the same fix TASK-029 /
      PR #2821 applied inside it. The shape: a merge calls
      `GetBooksBySeriesIDCore(fromID)` — the listing getter, which excludes
      non-primary versions — repoints every book it sees to `keepID`, then calls
      `DeleteSeries(fromID)`, leaving any non-primary version holding a series ID
      that no longer exists. **Two are live data loss.**
      (1) `internal/server/duplicates_helpers.go:454` `mergeSeriesGroupHelper` —
      no guard at all; `DeleteSeries` at `:476` is unconditional; reached from
      `executeSeriesNormalizeCore` at `:536`.
      (2) `internal/plugins/maintenance/series_denumber_op.go:286` — has a guard
      that cannot fire: `movedAll` is only set `false` inside the loop over the
      Core getter's rows, so a row that getter excludes can never flip it and the
      delete proceeds anyway.
      (3) `internal/maintenance/jobs/cleanup_series.go:222` `csMergeSeriesGroup`
      does **not** strand — its guard uses an independently-sourced *unfiltered*
      count (`GetAllSeriesBookRefCounts`, verified unfiltered in both the memdb
      and Pebble implementations), so it refuses the delete instead. But the
      refusal is permanent for those series, so those merges never complete.
      ⚠️ A getter swap alone will NOT fix (3): `refCounts` counts trashed rows
      while `GetBooksBySeriesIDAllVersions` excludes them, so any series with a
      trashed book still refuses forever. That one needs the count and the getter
      to share one predicate. Suggested split: one PR for (1)+(2), a separate one
      for (3). Full analysis in
      `docs/agent-tasks/todo-completion/handoff/2026-08-23-open-findings.md` §9.
      Raised while reviewing PR #2821 (TASK-029).
