- [ ] **COLLECTION-NAME-CONFLICT-SENTINEL** `PebbleStore.UpdateCollection`'s
      duplicate-name rejection still signals with a bare
      `fmt.Errorf("collection name %q already in use", ...)`, matched at call
      sites via `strings.Contains(err.Error(), "already in use")`
      (`internal/server/handlers/collections.go`,
      `internal/server/handlers/abs/collections.go`). Give it a sentinel —
      `var ErrCollectionNameInUse = errors.New(...)`, wrapped with `%w` — and
      switch those call sites to `errors.Is`, the way
      `ErrCollectionVersionConflict` now works in the same file.

  **Why this is worth doing rather than leaving as-is.** The version-conflict
  CAS was very nearly shipped with the same string match, on the argument that
  it matched the existing convention. It does not: `internal/database` already
  declares sentinels elsewhere (`ErrSettingNotFound` in `settings.go`,
  `ErrNoHNSWSnapshot` in `hnsw_embedding_store.go`), so `already in use` is the
  outlier, not the pattern. Converting the CAS turned up the concrete failure
  mode too — a test fake in `abs/collections_test.go` was hand-building a
  lookalike message, and would have gone on passing against a handler that had
  stopped recognising the error at all. The name conflict has exactly the same
  exposure today.
