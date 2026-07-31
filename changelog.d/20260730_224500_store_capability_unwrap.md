<!-- file: changelog.d/20260730_224500_store_capability_unwrap.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4e81c05a-3b76-4d29-9c14-8a2f6b0d7e93 -->
<!-- last-edited: 2026-07-30 -->

### Fixed

- **Capability-interface lookups no longer break behind the search-index store
  decorator.** `Server.Start()` replaces `s.store` with an `indexedStore` whenever
  the Bleve index opens successfully. Because that decorator embeds the
  `database.Store` *interface*, it promoted only that interface's methods and hid
  every narrow capability interface declared outside it — so
  `database.AsSyncIdentityStore`, `AsSyncFileStore` and `AsBookmarkStore` all
  started returning `nil` in production, with no compile error. Observed fallout:
  the `backfill-sync-ids` maintenance job failed outright with "store does not
  implement the sync-identity capability interfaces", and `internal/merge`'s
  sync-follow hook — which keeps a listener's position attached to a book across a
  dedup merge — silently degraded to a no-op. The lookups now walk the decorator
  chain via an opt-in `Unwrap`, and a new regression test asserts the real
  `indexedStore` preserves each capability. The wrap only happens when Bleve is
  active, which is why this reproduced on the production host and nowhere else.
- Added the missing compile-time assertion that `*PebbleStore` satisfies
  `SyncFileStore`, so a future signature drift fails the build instead of
  resurfacing as a runtime `nil`.
