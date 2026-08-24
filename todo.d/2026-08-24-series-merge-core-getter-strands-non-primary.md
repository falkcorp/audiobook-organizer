- [ ] **DEDUP-SERIES-MERGE-STRAND** Three series-merge paths *outside*
      `internal/dedup/series_dedup.go` had the shape TASK-029 / PR #2821 fixed
      inside it: a merge calls `GetBooksBySeriesIDCore(fromID)` — the listing
      getter, which excludes non-primary versions — repoints every book it sees
      to `keepID`, then calls `DeleteSeries(fromID)`, leaving any non-primary
      version holding a series ID that no longer exists. **Decision made and
      executed; this entry stays open only for the one PR still awaiting review.**
      - [x] (1) `internal/server/duplicates_helpers.go` `mergeSeriesGroupHelper`
        — live data loss, no guard at all, `DeleteSeries` unconditional. Fixed in
        **#2825**, which also converted `executeSeriesPrune`'s merge loop (a
        second stranding path in the same file, missed by the original survey)
        and `executeSeriesNormalizeCore`'s affected-book list.
      - [x] (2) `internal/plugins/maintenance/series_denumber_op.go` — live data
        loss behind a guard that could not fire: `movedAll` was only set `false`
        inside the loop over the Core getter's rows, so a row that getter
        excluded could never flip it. Fixed in **#2825**.
      - [ ] (3) `internal/maintenance/jobs/cleanup_series.go` `csMergeSeriesGroup`
        — did **not** strand. Its guard compared an unfiltered count
        (`GetAllSeriesBookRefCounts`) against a filtered read, so it refused
        instead — permanently, on every run. Fixed in **#2826**, held for review
        rather than merged: aligning the two predicates makes the job delete
        series it previously kept, which is a production-data judgement call.
        **Check this off when #2826 is merged or closed.**
      Full analysis in
      `docs/agent-tasks/todo-completion/handoff/2026-08-23-open-findings.md` §9;
      user-facing write-up in
      `docs/executive-summaries/2026-08-23-the-copies-the-merge-left-behind-executive-summary.md` §7.
      Raised while reviewing PR #2821 (TASK-029).

- [ ] **SERIES-MERGE-PRIMITIVE-UNGUARDED** `MergeSeries` — the store-level
      primitive beneath the paths above — has **no ref-count guard at all**.
      Every guard discussed in DEDUP-SERIES-MERGE-STRAND lives in a caller, so a
      new caller gets no protection by default and the safety property is
      re-implemented per site rather than enforced once at the bottom. Pre-existing
      and out of scope for #2825/#2826; noted so it is not lost. Decide whether the
      guard belongs in the primitive.
