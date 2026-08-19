### Fixed

- The ABS contributor-cache warm no longer races the search-index decorator. It
  waited for the memdb warmup through a bare `store.(*database.PebbleStore)`
  assertion read from *inside* a goroutine spawned at route-wiring time, while
  `Server.Start` later overwrites `s.store` with the Bleve `indexedStore`
  wrapper. `WaitForWarmup` is not part of `database.Store`, so the assertion
  succeeded against the bare store and failed against the wrapper: whether the
  warm waited at all was decided by scheduling. When it lost, the contributor
  cache was built against a half-published memdb and served that view of a
  library that does not exist for its whole TTL. The store is now read
  synchronously at wiring time and resolved through the decorator chain, so the
  unsynchronized read is gone rather than merely having both of its outcomes
  made correct. Note this path is not exercised under `-race` today — the
  wiring test reaches `wireABSRoutes` only with `ABSAPIEnabled` false, which
  returns before the warm — so the detector's silence was absence of coverage,
  not absence of a race.

### Changed

- The remaining bare `store.(*database.PebbleStore)` assertions in production
  code (scanner chapter extraction, dedup's ISBN/ASIN index wiring,
  `migration007Up`, and the db-health endpoint's Pebble section) now resolve
  through the decorator chain as well, leaving none outside tests. All four were
  traced and are reached with the **bare** store today, so none of them was
  broken — this is hardening so they stay correct by construction, since every
  one of them fails silently and looks like an unsupported backend rather than a
  bug.
