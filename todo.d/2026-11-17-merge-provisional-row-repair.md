- [ ] **TODO-MERGE-PROVISIONAL-ROW-REPAIR** Release stuck provisional auto-merge
      journal rows, and tell operators why a trashed book is being kept. A
      provisional `dedup:automerge:` row (written before a merge, finalized by the
      post-merge patch) that is never finalized or closed holds all its
      participants from every trash purge path for as long as it exists, and
      nothing releases it today. Add a Repairs-tab entry that lists live
      provisional rows older than the stale window (`provisionalStaleAfter`,
      `snapshot_pins.go`) and, for each, either FINALIZES it (the merge
      completed: find the actual winner from book state, then run the same patch
      the merge would have) or CLOSES it (the merge never wrote), dry run first.
      A provisional row has no `IntoGroupID` and the live participant is not
      unique after a failure inside `MergeBooks`, so the winner has to be found
      from book state, not from the row. Also surface the hold reason: all four
      purge callers drop the reason string from `undoHold.Holds`
      (`purge_run.go:188`, `service_single.go:534` and `:648`,
      `trash_progress.go:814`), and the single-book "discard progress and purge"
      path ends in `ErrPurgeUndoPossible` (`trash_progress.go:634`), which tells
      the operator to undo, while `UnmergeAuto` refuses the provisional row.
