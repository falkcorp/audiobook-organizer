### Fixed

- **Metadata fetch cache no longer replays a wrong-book result after the book's
  title or author is corrected (audit A3#14).** The per-provider fetch cache was
  keyed only on book + provider and had no invalidation path, so a result
  fetched while a book carried a garbage title kept being replayed (and could
  be re-applied) for the whole `MetadataFetchCacheTTLDays` window after the
  user fixed the title. Each cache row now records the search identity it was
  fetched for (normalized title, author, ASIN, ISBN-13, ISBN-10). A row with a
  different identity, or with no identity, is treated as a miss and is
  overwritten by the fresh fetch at the same key. Marking a book "no match"
  now also deletes its fetch-cache rows, because a rejected result is wrong
  for the book's unchanged identity and its stamp would still match. Applies,
  edits, undos and reverts do not delete them: an identity change already
  makes the stamped rows miss, and an apply that leaves the identity alone
  (narrator, series, fill-only) keeps rows that are still valid instead of
  forcing a provider refetch of every applied book. The bulk "skip already cached" probes check the same
  identity, so a book with a stale row is no longer skipped. New cache-miss
  reasons `no_identity` and `identity_mismatch` make the change visible in
  metrics.
- **Rollout (no mass refetch):** fetch-cache rows written before this change
  have no identity stamp. The bulk "skip already cached" checks count such a
  row as cached, so deploying this does not re-query every provider for the
  whole library; the TTL still applies, so legacy rows age out over
  `MetadataFetchCacheTTLDays`. An unstamped row is never replayed or applied:
  a single-book fetch, the search dialog and the bulk chain walk all treat it
  as a miss and re-query. A row stamped for a different identity is a miss
  everywhere, including the bulk skip, because the book's identity really
  changed.
