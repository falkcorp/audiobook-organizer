- [ ] **REVERT-PER-OP-LOCK** Reverting one operation is not serialized. Two Undo clicks at the same
      time (the Operations indicator and the Activity Log, or a double click) both call
      `RevertService.RevertOperation` (`internal/audiobooks/revert.go`), both read the same
      unstamped change rows, and both apply them: files are moved back twice and metadata is
      rewritten twice before either marks the rows. This race exists on main as well; #3312 did
      not introduce it. Add a per-operation lock (a keyed mutex on the operation ID, held from
      `GetOperationChanges` through `MarkOperationChangesReverted`), and have the second caller
      either wait and then see the rows already reverted, or get a 409 "revert already in
      progress". Add a race test that runs two reverts at once.
