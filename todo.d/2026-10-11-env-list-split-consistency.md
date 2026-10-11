- [ ] **ENV-LIST-SPLIT-CONSISTENCY** The string-list settings in `InitConfig` are read
      with `viper.GetStringSlice`, which splits an environment string on whitespace only,
      so the comma form that `PROTECTED_PATHS` now accepts does not work for its siblings.
      `SUPPORTED_EXTENSIONS=".m4b,.mp3"` becomes ONE entry `".m4b,.mp3"`, and because
      `len(configured) > 0` (`config.go` extension resolution) that entry replaces the
      default list, so a scan matches no files. `EXCLUDE_PATTERNS` and
      `FINGERPRINT_WORKER_TOOL_VERSIONS` have the same shape (`EXCLUDE_PATTERNS` also cannot
      hold a pattern with a space). `ENABLED_SORT_INDEXES` is documented as space-separated
      in `cmd/root.go`, `update_service.go`, `persistence.go` and `abs/browse.go`, so it
      must stay whitespace-split. Proposed fix, not implemented: a `viperStringList(key,
      sep)` helper beside `viperPathList` that takes the separator per key (comma for
      `SUPPORTED_EXTENSIONS`, `EXCLUDE_PATTERNS` and the tool-version list; whitespace for
      `ENABLED_SORT_INDEXES`) and trims entries. Also log a Warn when an entry of a
      whitespace-split list contains a comma (almost certainly a comma list written for the
      wrong key), and when a `PROTECTED_PATHS` entry contains ` /` (two absolute paths
      joined by a space, copied from the sibling key's documented form). Add a test per key
      with a comma list that must produce one entry per element. This predates #3955 (all
      of these were `GetStringSlice` on main), so it is its own change.
