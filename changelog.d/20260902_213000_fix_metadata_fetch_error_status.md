### Fixed

- **Bulk metadata fetch no longer records a rate-limited provider as a missing
  book.** The source-chain walk discarded the provider error, so a throttled
  response (429), a transport failure and an open circuit breaker all ended the
  walk the same way a genuine catalog miss does — with zero results — and every
  one of them was written to the operation ledger as `not_found`. With false
  misses in the ledger, "fetch only the books we are missing" could not be
  trusted, and the only safe recovery from a throttled run was a full re-scan of
  the library. Provider failures are now recorded as a distinct, retryable
  `fetch_error`, counted separately, and surfaced in the operation's progress
  line as `errors:N` alongside `cached:` and `not_found:`.
- The two bulk-fetch entry points (all-books and by-IDs) carried near-identical
  private copies of the source-chain walk, and the copies had already drifted:
  only the all-books copy retried with the untrimmed title when the chapter
  prefix had been stripped, so the same book could resolve differently depending
  on which path fetched it. Both now share one implementation.
- The 200ms inter-book pause was gated on a *successful* fetch, so it was skipped
  precisely when a provider was throttling us. It now also applies after a failed
  live call.
