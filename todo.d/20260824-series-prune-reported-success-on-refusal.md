- [x] **SERIES-PRUNE-REPORTS-SUCCESS-ON-REFUSAL** `executeSeriesPrune` returned
      `nil` unconditionally, so every entry in `mergeErrors` — including the
      fail-closed refusal added in #2828, whose message ends "Re-run after
      resolving the errors above" — reached the operator only as a `progress.Log`
      warn truncated to ten entries. `duplicates_ops.go` read the nil, set status
      `success` and emitted "Series prune completed"; `server_maintenance_deps.go`
      did the same for the nightly job. The guard worked and reported itself
      green. **Fixed 2026-08-24**, along with five siblings found in the same
      review:
      - [x] the organize loop in `duplicates_ops.go` dropped a book on
        `GetBookByID` returning `(nil, nil)` with no log, counter or error, while
        still counting it in "organizing the %d books it collected". Unrecoverable
        by re-running: normalize is idempotent on the series NAME, so a second run
        computes no actions.
      - [x] the canonical-series vote treated a failed count as zero books, so a
        transient read error decided which duplicate series got DELETED. Now
        disqualifies the group.
      - [x] the cached series list was invalidated only at the normal exit; five
        early returns bypassed it, all reachable after phase 1 had repointed
        books. Now deferred.
      - [x] a failed `GetAllSeries` refresh skipped the whole orphan sweep while
        the summary still said "0 errors".
      - [x] `computeSeriesNormalizeActions` swallowed a `GetAllSeries` failure and
        returned nil, indistinguishable from "library already clean" — it zeroed
        the dry-run PREVIEW too. Now returns an error.
      Two test gaps closed in the same PR: the `booksRepointed` cache predicate
      had **no** test that could detect its removal, and
      `series_prune_phase2_test.go`'s fixture returned static membership, so
      reverting phase 2 to the filtered counter (the 6,893-phantom-ID bug) stayed
      green. Both now fail on revert.

- [ ] **SERIES-NORMALIZE-PREVIEW-SWALLOWS-ERROR** `buildSeriesNormalizePreview`
      now logs the `computeSeriesNormalizeActions` failure instead of swallowing
      it, but still returns an empty preview — which an operator reads as "nothing
      to normalize" when deciding whether to approve a run. Giving it a real error
      channel needs a handler signature change (it feeds an injected closure the
      duplicates sub-package calls, and that closure has no error return). Decide
      whether the preview endpoint should 500 on a failed listing.
