<!-- file: changelog.d/20260731_172500_storecap_concrete_store.md -->
<!-- version: 1.0.0 -->
<!-- guid: c4d8e1a6-7f39-4b52-9e08-2a6b5d3c9f71 -->
<!-- last-edited: 2026-07-31 -->

### Fixed

- **Two maintenance jobs had been silently degrading in production, and the
  library-wipe fixups were failing outright.** All three are the concrete-type
  half of the decorator bug fixed for capability interfaces in the previous
  release: `Server.Start()` replaces the store with a Bleve search-index
  decorator, and a bare `store.(*database.PebbleStore)` assertion fails against
  that wrapper just as an interface assertion does. Because each of these call
  sites had a deliberate "different backend" fallback written for SQLite and test
  doubles, a wrapped Pebble store was indistinguishable from an unsupported
  backend and the fallback made it look like a supported configuration:
  - `sweep-pebble-metrics-ttl` logged "store is not a PebbleStore; skipping" and
    no-opped on every production run, so expired Pebble metrics snapshots were
    never swept and grew without bound past their 30-day retention window.
  - `recompute-book-aggregates` took its interface fallback every run, which
    skips the `book_aggregates_v1_done` sentinel — so instead of short-circuiting
    on an already-completed backfill it recomputed all ~40k books each time.
  - The six library-wipe fixups either failed with "unsupported store type
    \*server.indexedStore" (`wipeSegments`, `wipeBooks`, `wipeAuthors`,
    `wipeSeries`, `wipeExternalIDs`) or fell back to a slower interface loop that
    reports an approximate count and misses the secondary-index prefixes
    (`wipeBookFiles`).

  All eight sites now resolve the concrete store through the new
  `database.AsPebbleStore`, and a regression test drives the repaired wipe
  fixups through the real `indexedStore` — it reproduces the exact production
  error string if the bare assertions are restored.

### Changed

- `database.asCapability` is now exported as `database.AsCapability`, since the
  same decorator problem has to be solved from `internal/server` and
  `internal/maintenance/jobs` too. It works for concrete types as well as
  interfaces.
- `GetOpsV2` and `GetAIJobs` now delegate to `AsCapability` instead of each
  carrying its own hand-inlined copy of the unwrap walk. Their behaviour is
  unchanged, and the walk is now depth-bounded — the old loops would spin forever
  on a decorator whose `Unwrap` returned itself.
- Documented, and pinned with a test, the distinction that explains why this bug
  hit some capabilities and not others: `OpsV2Store` and `AIJobsStore` are
  subsets of `Store`'s method set, so any decorator embedding the `Store`
  interface satisfies them directly and they were never at risk. Only
  capabilities with at least one method outside `Store` (`SyncIdentityStore`,
  `SyncFileStore`, `BookmarkStore`, and the concrete `*PebbleStore`) are
  affected. The test fails if a capability ever crosses that line.
