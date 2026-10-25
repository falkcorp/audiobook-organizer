- [ ] **TODO-RETIRED-UNDO-DEFERRED** the undo change (PR 5) built the journal
      and the revert for a retire but left things to later changes.
      (1) `undo.PurgedShellRecreator` has no store implementation: until the
      purge change (PR 6) journals a `book_purge_snapshot` and implements
      `HasPurgeSnapshot` and `RecreatePurgedBook` (and marks the purge's own
      change row reverted), a retire whose shell was purged refuses with
      `book_missing`, which is the safe answer. The recreate branch is covered
      with a fake only. PR 6 also owes the PLAN 6.2 snapshot completeness
      test (every `Book` field and every key family hanging off a book is
      either in the snapshot or listed as derived). (2) The fixer Apply (PR 7)
      must call `repairs.Writer.RetireVersion` and `RehomeRetiredVersion`,
      journal the shell's external-id moves and merged-into pointer with the
      existing `Step`/`Journal` (as `retire_into.go` does), hold
      `merge.LockMergeRMW`, and pass `CombineJournalID` when the retire is
      part of a combine so `UndoCombine` can reverse it. Until PR 7 lands
      nothing writes a `merge:combine-retire:` link, so the combine undo
      path for retires is exercised by tests only. (3) The undo UI does not
      yet offer `RevertOperationForBooks` per shell; the endpoint wiring
      belongs with the UI change (PR 9).
      (4) A whole-operation preflight (`undo.PreflightUndoConflicts`) of a
      retire op judges the retire rows of a version against the holder the
      rehome rows of that SAME operation put it back on
      (`RevertPlan.RehomeHolder`); a retire op whose version was rehomed by a
      LATER operation still reports its rows "changed since", as the revert of
      the earlier op refuses them: reverse the later operation first.
      (5) The combine undo still refuses a retire that also rehomed a
      version the shell's own retire created; only the rehome of a version the
      shell held before is reversed with it. (6) PR 7 must put the shell in the
      trash before calling `RetireVersion` (it now refuses a live shell).
      (7) The retire journal rows are exempt from journal pruning, but the
      retire op's own `external_id_reassign` and `book_merged_into` rows are
      not. `PruneOperationChanges` has no production caller today, so nothing
      is lost yet; whichever change wires a prune must exempt those two types
      for an op that also has retire rows, or an old retire op is read as
      partly reversed.
      (8) Left from the second re-review of PR 5, none reachable until PR 7
      writes a retire. (a) "Owed beats pending" in `mergeSettleOwedGroup`: when
      a group is owed by a failed settle, a dead scoped run's already-restored
      rows are not evidence, so the later-pick check can drop the settle and
      leave two primaries with only an Info log. This matches the in-record
      rule `markSettlePending` already has and keeps a user's pick; it is an
      owner decision whether to change it. (b) `applyUndo` reverses the
      `merge:combine-retire:<cj>:<shell>:<op>` link keys in key order, which
      is chronological only for ULID op ids; it fails closed, so add a comment
      or sort by the ops' `CreatedAt`. (c) The "given back by a revert outside
      the undo" subtest in `combine_retire_undo_test.go` passes on the older
      code too (its fake carries no reverted ids); it documents the case and
      cannot catch a regression.
