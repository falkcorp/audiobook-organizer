- [ ] Add a Settings panel for `path_aliases` (root / Windows prefix / UNC /
      smb URL). v1 is config-and-seed only, so changing an alias means editing
      config. See `docs/design/2026-08-20-dual-path-display.md` open question 1.
- [ ] Make `PathAliases` the single source for the Windows prefix and have
      `reconcile.TranslateITunesPath` read from it, retiring the duplication
      that `ValidatePathAliases` currently only guards against.
- [ ] Reset the module-scope `cachedAliasesPromise` in `PathLinks.tsx`
      (and `cachedVarsPromise` in `formatPath.ts`) between tests. Both
      caches persist across a test file, so today every test shares one
      seeded alias set; a future test needing different alias data per
      case will get a stale answer with no obvious cause.
- [ ] Decide how `path_aliases` re-derives after a normalization change.
      `SeedPathAliases` short-circuits on `len(aliases) > 0`, so once a value is
      persisted it is never re-seeded, and `ValidatePathAliases` cannot tell a
      stale persisted value from a correct one. Harmless today (the feature has
      never been deployed, so no config_blob holds a pre-normalization value),
      but any future change to `normalizeWindowsPrefix` inherits the same
      problem — a stored alias will not pick it up.
