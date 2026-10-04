### Fixed

#### Import roots come from the store the resolver is given, not from a process global (fixes the `TestApplyCachedCandidate_GateRefuses` flake)

The metadata search-title resolver treats every import path as a root and never lists it for sibling rows. Until now it got those paths from a package global, `metabatch.SetImportRootsSource`, which `server.NewServer` set to a closure over its store. The registration outlived the store. About 38 `internal/server` test files call `NewServer` directly and close their store without clearing the global, so a later test resolving through the apply path called `GetAllImportPaths` on a closed Pebble store and panicked with `pebble: closed`.

Whether that happened depended on timing. If the closed store's async memdb warmup had published, the read was served from memdb and passed. If it had not, the read fell through to Pebble and panicked. That is why the test failed about 1 run in 3 in full-package runs and only under load.

- `metabatch.SearchQueryReader` now includes `ImportPathReader` (`GetAllImportPaths`). The resolver reads roots from the same store the caller already passes it. A `FolderMemo` reads them once per minute under a lock, so concurrent workers wait for the first load instead of reading an empty list. A memo-less call (the apply and gate paths) reads them at most once per row, and only if the row reaches a root check. A failed read is logged and not cached.
- Removed `SetImportRootsSource`, `registerImportRootsSource`, the generation, TTL and first-load-wait machinery, and the `t.Cleanup` calls in the server test helpers that existed only to unset the global.
- The resolver tests used `/imports/incoming` as their import root. `incoming` is a generic folder name, so they passed without ever reaching the import-root check. They now use a non-generic name, and disabling the root check fails 6 of them.
- Repro: 10 concurrent processes each running the AI-jobs and author-ref-audit tests (both call `NewServer`) followed by the victim test. Before: 3/50 panicked with `pebble: closed`. After: see the PR.
