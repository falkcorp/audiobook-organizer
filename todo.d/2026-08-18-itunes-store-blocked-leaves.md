- [ ] **Narrow `positionSyncStore` and `pathRepairerStore` — both blocked on a wide
      parameter type in another package, not on their own declaration.** These are
      the two of six iTunes subsystem stores left wide after the first narrowing
      pass. Direct calls are small (`positionSyncStore` 8, `pathRepairerStore` 5);
      what holds them wide is what they get passed to:
      - `readstatus.RecomputeUserBookState` and `readstatus.SetManualStatus` take an
        **anonymous composite** `interface{database.BookFileStore;
        database.UserPositionStore}` inline in their signatures. An anonymous
        interface cannot be narrowed in place or nolint-ed, and `interfacebloat`
        does not report it because it is not a declaration. Give it a name and
        narrow it to what `readstatus` actually calls.
      - `pathRepairerStore` is additionally passed somewhere wanting the whole
        `database.OperationStore`, plus `operations.OperationStateDeleter`,
        `pidLookup` and `tierAStore`.
      This is the #2552 lever one package out: fix the parameter types and the two
      leaves narrow themselves.
- [ ] **Re-probe `itunesservice.Store` after those two land.** Its measured
      requirement was computed against 151-method leaves, so it is stale by
      construction. Only then decide whether `Store` composes from the six
      subsystem interfaces or should be replaced by per-consumer interfaces —
      its 8 remaining methods (`CreateAuthor`, `CreateSeries`, `GetSeriesByName`,
      `SetBookAuthors`, `IsHashBlocked`, `SaveLibraryFingerprint`,
      `GetPendingDeferredITunesUpdates`, `MarkDeferredITunesUpdateApplied`) are
      import-pipeline writes belonging to none of the six.
