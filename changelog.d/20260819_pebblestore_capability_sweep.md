### Changed

- No production code outside `internal/database` names `*database.PebbleStore`
  any more. The remaining 10 `database.AsPebbleStore` call sites — db-health's
  Pebble section, the book-aggregates backfill sentinel, the AcoustID
  fingerprint reset, scanner chapter persistence, dedup's ISBN index wiring, and
  five sites that only wanted the raw `*pebble.DB` handle — now go through named
  capability interfaces or new store-taking constructors. No behaviour change:
  `AsPebbleStore` already walked the decorator chain, so every site resolves
  exactly as before. What changes is that resolving the concrete store is now
  something only `internal/database` does, which is the precondition for ever
  splitting that type (`docs/plans/2026-08-19-split-the-pebblestore-surface.md`).
- `database.NewEmbeddingStoreFromStore`, `NewPebbleMetricsStoreFromStore`,
  `NewAIScanStoreFromStore` and `NewPebbleActivityStoreFromStore` are new. Their
  `*pebble.DB`-taking twins forced five packages to unwrap the store themselves;
  handing those callers an `interface{ DB() *pebble.DB }` would have traded a
  dependency on our concrete type for one on pebble's, so the unwrap moved into
  `internal/database` instead.
