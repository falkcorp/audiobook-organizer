### Fixed

- Search no longer serves stale titles after edits made by registry-built
  services. `NewServer` handed the service registry the bare store and only
  wrapped `s.store` in the search-indexing `indexedStore` later, in `Start`, so
  every registry service (audiobooks, metafetch, merge, plugin ops) wrote books
  that were never re-indexed. The decorator is now built once in `NewServer`,
  before the registry, and shared by the registry, the inline services, the
  scanner, the file-I/O pool, the extra-ops registrar and the global store;
  `Start` only switches its queue and worker on (`startSearchIndexing`), so no
  write is indexed twice.
- Capability lookups that the registry-held store could no longer satisfy by
  bare type assertion now go through `database.AsCapability`: the ABS route
  wiring (which would have exited at boot), the dedup `lsh-index-build`,
  `bookfile-seg-drop` and `full-scan` ops, and the scan-cache backfill endpoint
  (which already answered 501 in production for the same reason).
