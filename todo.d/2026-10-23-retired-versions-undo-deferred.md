- [ ] **TODO-RETIRED-UNDO-DEFERRED** the undo change (PR 5) built the journal
      and the revert for a retire but left three things to later changes.
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
      chain operation (a retire whose version was later rehomed to another
      holder) reports the retire rows "changed since", because the version
      they name is on the new holder until the rehome is reversed and the
      check reads state, not the reversal order. The revert itself is right;
      only the prediction is not, with a live old holder as well as a trashed
      one. (5) The combine undo still refuses a retire that also rehomed a
      version the shell's own retire created; only the rehome of a version the
      shell held before is reversed with it. (6) PR 7 must put the shell in the
      trash before calling `RetireVersion` (it now refuses a live shell).
