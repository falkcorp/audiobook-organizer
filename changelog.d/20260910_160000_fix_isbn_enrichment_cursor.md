### Fixed

- **The nightly ISBN/ASIN enrichment sweep now advances through the whole library
  instead of re-checking the same first ~100 books every run.** The batch job
  restarted from the beginning on every run and stopped after a fixed limit, so it
  could never reach books past the front — a stall made total once the recent
  ASIN-gate fix widened the set of books it considers. The sweep now remembers where
  it left off (a persistent last-seen-book cursor) and wraps around to the start once
  it reaches the end.
- **Books whose title is blank but whose audio intro was transcribed can now acquire
  an identifier.** Enrichment falls back to the transcribed title when the stored
  title is empty, instead of searching with an empty query that can never match.

### Changed

- The enrichment batch size is now read from an optional `isbn_enrichment_batch_limit`
  setting (default 100), so it can be tuned without a deploy. With the setting unset,
  behavior is unchanged.
