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
  `ErrProtectedRenameSource`), logged at Warn at most once a minute. Only an
  explicit `SourceMovable` clears a source: the zero value of `SourceVerdict` and
  any verdict the code does not know refuse (outcome `unknown_guard_verdict`,
  `ErrRenameGuardVerdictUnknown`). With no guard installed (tests, tools) nothing
  is asked and nothing is refused, as before. The server reads whether the Deluge
  list has loaded before it runs the protected check, so a refresh that lands in
  between cannot make a seeding file look movable.
- **The purge protects at least as much as a fresh look, for both inputs.**
  `pathutil.UnderProtectedPath` reuses folders resolved up to 5 s ago, and the
  audiobooks helper reuses an import-path list read up to 5 s ago, so inside that
  window a protected folder's symlink re-pointed, or an import folder added under
  the library root, could make the book purge and the version purge protect less
  than a fresh look. They now resolve the folders afresh
  (`pathutil.UnderProtectedPathUncached`) and read the import paths straight from
  the store (`GetAllImportPaths`) in `planPurgePathIn`; a list that cannot be read
  still holds the file back for a retry. Tag writes, previews and organize keep
  both caches.
- **A protected folder that keeps failing to resolve is logged again after an hour**
  (it was logged once per process and then never), and `RootError.Root` is now always
  the folder as configured, so a folder that could not be made absolute is cleared
  from the "already reported" set when it resolves again. Callers still served a
  cached failed set after the folder has resolved (the purge's fresh look clears
  it) no longer log it on every call for the rest of the cache window.
- The `UnderProtectedPath` documentation now says `deluge.ProtectedPathCache` also
  holds `config.ProtectedPaths`, and that both are still compared as spelled.
