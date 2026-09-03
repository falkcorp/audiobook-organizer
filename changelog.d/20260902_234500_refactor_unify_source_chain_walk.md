### Fixed

- **The third copy of the provider search walk now reports throttling correctly
  too.** The "search every metadata provider for one book" logic existed as three
  near-identical private copies — two in the v2 bulk-fetch operation and one in
  the registered `bulk_fetch_metadata` maintenance job — and all three
  independently discarded the provider error, so a rate-limited response was
  recorded as a missing book. Fixing the operation left the maintenance job
  still writing false misses into the same ledger. All three now share one
  implementation, so the error/miss distinction, the untrimmed-title retry, the
  per-provider concurrency bound and the throttle backoff apply on every path.
- The maintenance job previously issued its provider calls with **no
  per-provider concurrency bound at all**, so it could stampede a provider the
  v2 operation was being careful with. It now uses the same semaphore.

### Changed

- `WalkSourceChain`, `ChainOutcome` and `ProviderSemaphore` moved to
  `internal/metafetch` so the server operation and the maintenance job can share
  them.
- Removed a fourth copy of the chapter-prefix title stripper
  (`bmf_stripChapterFromTitle`), left dead by the unification and byte-identical
  to the one it duplicated.
