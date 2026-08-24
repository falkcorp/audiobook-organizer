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

- [ ] **SERIES-MERGE-TRASHED-ROWS-RESIDUAL** All three paths fixed by #2825 still
      strand **trashed** rows. Both series getters exclude soft-deleted books by
      design, so a series holding one live book and one trashed book is deleted
      with the trashed row still pointing at it — restore it from the trash later
      and it has no series. The tooling to close this already exists and is
      already used **in the same function**: `executeSeriesPrune`'s phase 2
      (`internal/server/duplicates_helpers.go`) obtains
      `database.AsSeriesBookRefStore(store)` and fails closed, with a comment
      calling the filtered fallback *"the failure family this repo keeps
      rediscovering"* — while phase 1, sixty lines above, deletes with no such
      guard. `internal/maintenance/jobs/cleanup_series.go` uses the one-line
      `database.SeriesRefCounts(store)` wrapper for exactly this.
      ⚠️ **Not** done in #2825 on purpose: gating phase 1 on the unfiltered count
      makes the prune **refuse merges it currently completes**, which is the same
      class of production-data behaviour change #2826 is being held for. Decide
      both together or neither.

- [ ] **SERIES-NORMALIZE-WRITEBACK-SPLIT** `executeSeriesNormalizeCore` returns
      ONE list that feeds two different consumers with two different policies:
      `ReOrganizeInPlace` (which must exclude non-primary versions — the
      organizer's own filter at `internal/organizer/service.go:640` says so, and
      the default naming patterns give a primary and its alternate rip an
      identical target path) and the tag write-back (which arguably should
      include them, since a repointed alternate rip now carries stale series
      tags). #2825 briefly widened the list to the complete set, which silently
      overrode organize policy; that was reverted. The proper fix is to return
      two lists. ⚠️ It would start writing tags to files this op has never
      touched — a production-data decision, hence not done unilaterally.

- [ ] **SERIES-MERGE-PRIMITIVE-UNGUARDED** `MergeSeries` — the store-level
      primitive beneath the paths above — has **no ref-count guard at all**.
      Every guard discussed in DEDUP-SERIES-MERGE-STRAND lives in a caller, so a
      new caller gets no protection by default and the safety property is
      re-implemented per site rather than enforced once at the bottom. Pre-existing
      and out of scope for #2825/#2826; noted so it is not lost. Decide whether the
      guard belongs in the primitive.
