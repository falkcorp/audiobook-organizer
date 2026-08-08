<!-- file: changelog.d/20260807_203700_fix_memdb_warmup_caller_pointer_race.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6b2e4f9a-1d70-4c38-8a5e-3f96c0d7e2b1 -->
<!-- last-edited: 2026-08-07 -->

### Fixed

- **Memdb warmup caller-pointer data race.** `UpsertBookToMemDB` captured the
  caller's `*Book` in the write-through closure; while the async warmup was
  still buffering, that closure ran much later on the warmup goroutine
  (`publishWarmMemStore` → `applyMemSync` → `stripBookForMemdb`'s `cp := *src`),
  reading the caller's live struct while the caller was still mutating it
  (`UpdateBook` writes `book.ID` after the call returns). Under load during
  startup — exactly when backfills and migrations run — this could write a torn,
  half-updated Book projection into memdb. Fixed by snapshotting the struct at
  enqueue time, and the same copy-at-enqueue rule was applied to every sibling
  helper with the same shape (`UpsertBookFileToMemDB`, `UpsertAuthorToMemDB`,
  `UpsertSeriesToMemDB`, `UpsertNarratorToMemDB`, `UpsertImportPathToMemDB`,
  `UpsertAuthorAliasToMemDB`, `UpsertBlockedHashToMemDB`, plus slice copies in
  `ReplaceBookAuthorsInMemDB`/`ReplaceBookNarratorsInMemDB`). Regression test
  `TestUpsertBookToMemDB_SnapshotsCallerBookAtEnqueue` forces the exact
  interleaving deterministically under `-race` instead of hoping to catch it.
