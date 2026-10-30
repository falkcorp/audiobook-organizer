### Fixed

- **In-place moves wait for the Deluge list instead of calling the file protected.**
  The rename-source guard (`organizer.SetRenameSourceGuard`) now answers with a
  verdict (`SourceMovable`, `SourceProtected`, `SourceListNotLoaded`) instead of a
  bool. While Deluge is configured but its seeding list has not loaded, an in-place
  re-organize is still refused (fail closed) but is counted under its own outcome,
  `protected_list_not_loaded`, with the reason "the Deluge seeding list has not
  loaded yet; try again once it has", instead of `protected_source` ("the file is in
  a protected location..."). It is a skip, never a failure, and the book moves on the
  next run once the list has loaded. A path that is protected is still reported as
  `protected_source` while the list is down. A run logs one Warn with the count of
  moves held back (the per-book line stays Debug). `RenameFiles` holds a plan back
  for the same reason with the new `ErrRenameProtectedListNotLoaded` (not
  `ErrProtectedRenameSource`), logged at Warn at most once a minute.
- **The purge always resolves the protected folders afresh.** `pathutil.UnderProtectedPath`
  reuses folders resolved up to 5 s ago, so a protected folder's symlink re-pointed
  inside that window could make the book purge and the version purge protect less
  than a fresh look. They now use the new `pathutil.UnderProtectedPathUncached`
  (`planPurgePathIn`); tag writes, previews and organize keep the cache.
- **A protected folder that keeps failing to resolve is logged again after an hour**
  (it was logged once per process and then never), and `RootError.Root` is now always
  the folder as configured, so a folder that could not be made absolute is cleared
  from the "already reported" set when it resolves again.
- The `UnderProtectedPath` documentation now says `deluge.ProtectedPathCache` also
  holds `config.ProtectedPaths`, and that both are still compared as spelled.
