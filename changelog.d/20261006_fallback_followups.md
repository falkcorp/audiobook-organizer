### Changed

- **Candidate-fetch fallback follow-ups (owner decisions 2026-10-06).**
  - The Open Library / Google Books fallback now runs when the chain leaves
    a book with **no usable candidate**: none, all owner-rejected, all
    refused by `asin_conflict` / an ASIN-replaced `identity_stale`, or the
    best below the apply floor. Rows that held only such candidates (served
    from the cache forever, never selected) are now selected by the
    scheduled fetch. Fallback candidates are merged into the row
    (`SearchOptions.MergeWithCached`), never replace the chain's, and a
    forced or stale refetch of the chain keeps them.
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
  - **Open Library and Google Books candidates are review-only** (owner
    decision): the apply gate refuses them with `review_only_source` in
    every unattended apply (bulk apply, metadata upgrade), the transcription
    auto-apply neither offers nor writes one, and an owner review applies
    one by hand (never past `identity_stale`).
  - **"Search again" on one book is interactive** (owner decision): the
    review page marks it (`interactive` on `batch-fetch-candidates`, honored
    for exactly one book id), so its Google lookup may use the 200 reserved
    for interactive use. Any multi-book selection stays background (800).
  - A fallback merge no longer replaces the chain's candidates on a row
    written before `fetched_for_asin` existed, or on a row the batch verdict
    vouched for under other hashed inputs (a raw author credit, a
    pre-2026-09-28 no-author row). Merging into a legacy or prior-rule row
    keeps its position filter, its fingerprint (legacy) and its
    `FetchedAt`, so a filtered sibling never comes back as a fresh,
    current candidate.
  - A merge ranks usable candidates above refused ones (owner-rejected,
    `asin_conflict`, below the floor), so the row's first candidate is one
    the owner can use. The row's read and write are locked per book, so a
    concurrent search-dialog fetch is not undone.
  - The scheduled selection funds Google per request, and a book Open
    Library still owes is asked of Open Library even when the day's Google
    share is spent (its Google step waits, unrecorded).
  - The "deferred" chip counts a book only while it still has no usable
    candidate, and a usable answer clears the deferral.
  - The Google budget is attached before the server is built and in the
    operation-runner child process; a budget whose count cannot be read or
    saved refuses without a throttle hold. A fallback whose candidates are
    all ranked out of the row's top 10 is recorded as `ranked_out`, not a
    match; the trigger uses the apply gate's full score rule.
