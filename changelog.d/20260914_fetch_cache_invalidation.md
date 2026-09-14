### Fixed

- **Metadata fetch cache no longer replays a wrong-book result after the book's
  title or author is corrected (audit A3#14).** The per-provider fetch cache was
  keyed only on book + provider and had no invalidation path, so a result
  fetched while a book carried a garbage title kept being replayed (and could
  be re-applied) for the whole `MetadataFetchCacheTTLDays` window after the
  user fixed the title. Each cache row now records the search identity it was
  fetched for (normalized title, author, ASIN, ISBN-13, ISBN-10). A row with a
  different identity, or with no identity, is treated as a miss and is
  overwritten by the fresh fetch at the same key. `InvalidateCachedCandidates`
  (manual edit, metadata apply, organize rename) now also deletes the book's
  fetch-cache rows. The bulk "skip already cached" probes check the same
  identity, so a book with a stale row is no longer skipped. New cache-miss
  reasons `no_identity` and `identity_mismatch` make the change visible in
  metrics.
- **Rollout note:** every fetch-cache row written before this change has no
  identity stamp, so all of them read as misses after deploy. The first bulk
  metadata fetch after deploy re-queries every provider for every book it
  covers.
