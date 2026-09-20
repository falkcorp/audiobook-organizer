- [ ] **TODO-TITLEDIR** Index `GetBooksByTitleInDir` — it full-scans every book
      row once per newly imported book, inside the scan's version-link lock.
      `internal/database/pebble_store.go:1556` iterates and JSON-decodes the
      entire book keyspace (`forEachBookRow`) to find same-title siblings in one
      directory; its own comment concedes "Always scans Pebble — MemStore has no
      title+dir index". `internal/scanner/scanner.go:3227` calls it for **every
      new book** on the import path, so cost is (new books) × (all book rows):
      against a ~76k-row library, importing a 1,494-file folder is up to ~114M
      row decodes for that one folder. Worse than a bare O(n²): the call sits
      inside `lockVersionLinkFor(dbBook, parentDir)`, so each full scan is held
      under a folder+title stripe and serializes concurrent imports into the
      same folder. The hazard was already written down and then violated —
      `GetFolderDuplicatesCore` (`pebble_store.go` ~1592) documents that it
      deliberately never calls this method because doing so per book is "that
      O(N^2) shape". Fix direction: give MemStore a `(normalizedTitle,
      parentDir)` index and delegate when published (the same shape
      `GetBooksBySeriesIDCore` and `GetFolderDuplicatesCore` already use), or at
      minimum replace the full keyspace walk with an iterator bounded by a
      dir-prefixed key range. Pre-existing; surfaced by the #3481 review, not
      caused by it.
