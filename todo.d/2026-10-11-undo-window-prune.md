- [ ] **UNDO-WINDOW-PRUNE** Give the undo journal (`opchange:` rows) its own
      retention, the "undo window" of
      `docs/design/2026-10-03-storage-efficiency-design.md` section 7 ("Separate
      settings", about line 839). Nothing prunes the journal today: on
      2026-10-11 the `PruneOperationChanges` step was taken out of the log
      purge (`logger.PruneOldLogs`) before it ever ran on prod, so no log
      setting can shorten undo. `PebbleStore.PruneOperationChanges` (chunked,
      cancellable, with a scan heartbeat) is kept, unwired, for this. Before
      anything calls it: owner sign-off on the window; the first run is a dry
      run that only counts and summarises for the owner; every run is capped;
      `book_file_delete` / `book_file_repoint` rows are never deleted until the
      release C ledger scan (section 9) has stored their signal bytes; and the
      Repairs fixers that read old journal rows (fragment `scanPlanJournal`,
      `resumeHandOff`, the version-twin fixer) are checked against the window.
