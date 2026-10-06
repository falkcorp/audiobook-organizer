### Changed

- **Candidate-fetch fallback follow-ups (owner decisions 2026-10-06).**
  - The Open Library / Google Books fallback now runs when the chain leaves
    a book with **no usable candidate**: none, all owner-rejected, all
    refused by `asin_conflict` / an ASIN-replaced `identity_stale`, or the
    best below the apply floor. Rows that held only such candidates (served
    from the cache forever, never selected) are now selected by the
    scheduled fetch. Fallback candidates are merged into the row
    (`SearchOptions.MergeWithCached`), never replace the chain's.
  - **One shared Google Books daily budget** for every caller
    (`internal/metadata/dailyquota`), enforced in Google's HTTP transport
    per request sent: `google_books_daily_limit` (default 1,000) total,
    `google_books_background_daily_limit` (default 800) for background
    lookups; interactive lookups (search dialog, test connection) may use
    the rest. A budget refusal never trips the circuit breaker or a
    throttle hold. `google_books_fallback_daily_limit` now only turns the
    fallback's Google step off (negative) or lowers the background cap.
  - A failed title-searching chain source (not the ASIN-only Audnexus)
    refuses the fallback; an Open Library failure moves on to Google; only
    429/5xx/timeouts/holds/budget defer, a permanent 4xx is an error and is
    remembered on the row (`fallback_attempts`); the Google-capped selection
    goes oldest attempt first and counts books owed both providers.
  - Owner-manual-only books still ask Open Library (Google skipped); an
    unreadable manual check defers instead of reading as no_match.
  - Fallback searches no longer make Audible/Audnexus ASIN lookups.
  - `deferred` and `skipped` counters on the operation-results and
    recent-fetches endpoints; a "deferred" chip on the review page.
  - Lost-candidates fixer no longer says "providers returned nothing" for a
    deferred refetch.
