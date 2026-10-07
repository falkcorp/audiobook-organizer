### Changed

- **Candidate-fetch fallback follow-ups (owner decisions 2026-10-06).**
  - The Open Library / Google Books fallback now runs when the chain leaves
    a book with **no usable candidate**: none, all owner-rejected, all
    refused by `asin_conflict` / an ASIN-replaced `identity_stale`, or the
    best below the apply floor. Rows that held only such candidates (served
    from the cache forever, never selected) are now selected by the
    scheduled fetch. Fallback candidates are merged into the row
    (`SearchOptions.MergeWithCached`) instead of replacing the chain's, and
    a forced or stale refetch of the chain keeps them. The merged row is
    still capped at 10: see the eviction order below.
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
  - The hashless `review_bulk` owner marker (a bulk-applied book whose
    candidate the review lane never loaded) no longer lifts
    `review_only_source`: an Open Library / Google Books candidate is
    applied only on a pin of the candidate the owner was shown
    (`applygate.Verdict.UnseenOwnerReviewOverridable`).
  - **An empty chain refetch no longer wipes stored candidates** of a row
    hashed under other inputs (a raw author credit, a pre-2026-09-28
    no-author row). The scheduled fetch selects such rows when no candidate
    is usable, and an empty answer used to write `Candidates: []` over them
    every tick; a forced refetch did the same. `MergeFromSourceHash` is now
    `SearchOptions.CarryFromSourceHash`, honored by the merge, the
    preserve-on-empty and the fallback-carry paths, and the batch fetch
    passes the hash of the row `metafetch.Service.VouchedCachedRow` vouches
    for (forced or not). A row of another identity is neither carried nor
    merged into.
  - Merge ranking is tiered (`SearchOptions.MergeRank`,
    `metabatch.MergeRanker`; was `MergeUsable`): usable candidates the gate
    may apply unattended, then usable review-only ones, then
    refused-but-reviewable ones (below the floor, `asin_conflict`), then
    owner-rejected ones; score orders each tier. When the union exceeds 10
    the bottom is evicted: undecodable rows, then owner-rejected, then
    refused-but-reviewable by lowest score; a usable candidate only when
    more than 10 better-or-equal ones exist. A chain candidate can still be
    evicted (10 below-floor Audible + 2 usable Google keeps 8 Audible).
  - A merge into a row fetched for **another ASIN** replaces that row
    instead of merging: its candidates answered a record the book no
    longer carries.
  - Books the chain has not answered (never fetched, a stale empty row)
    are funded against the day's background Google share after every book
    already owed a Google lookup; an unfunded one is still selected, with
    its Google step put off (`ChainCapped`).
  - **Auto-fetch fetches but never applies Open Library / Google Books**
    (owner decision): `FetchMetadataForBook` (organize, the iTunes import
    enrichment, the single-book "Fetch metadata" button) and
    `FetchMetadataForBookByTitle` (the production-company resolvers) still
    search them, but the chain moves on to the next source instead of
    applying; with no other match they return
    `metafetch.ErrReviewOnlyCandidatesNotApplied` (a
    `ReviewOnlyNotAppliedError` naming the sources) and write nothing to
    the book. `FetchMetadataForBook` leaves the answer in the per-provider
    fetch cache, which the batch candidate fetch replays when it next
    reaches the book: that is when the match shows on the review page, not
    at once. `FetchMetadataForBookByTitle` caches nothing. The single-book
    button answers 200 "Match found, left for review" (`review_only`), not
    a 404, and shows it as an info toast.
    `POST /metadata/bulk-fetch` applies the best non-review-only candidate
    or reports `review_only`. The iTunes enrichment does not count that
    answer toward its rate-limit breaker.
  - **An empty refetch keeps the candidates of two more row shapes**: a
    legacy row with no `SourceHash` (forced refetch), and a plain-fetch row
    hashed with no author or narrator (forced or not).
    `VouchedCachedRow` now vouches a hashless row the way the gate does, and
    a plain-fetch row by its search fingerprint; the carry is an explicit
    flag (`SearchOptions.CarryFromRow`, set by `SearchOptions.CarryFrom`),
    so an empty hash can be carried. The batch verdict and the gate are
    unchanged.
  - The all-cached (select-all) bulk-apply preview reports
    `owner_reviewed_would_apply` under the hashless marker's rule, the one
    its apply runs under, so it no longer says a review-only candidate would
    apply. A listed-book preview keeps the single-row Apply's rule.
  - A refetch with no merge ranker (search dialog, stale refetch,
    lost-candidates fixer, single-book fetch) that keeps a fallback
    provider's candidates now ranks Open Library / Google Books candidates
    after the chain's (`reviewOnlyLastRank`). Ranking by score alone let a
    higher-scoring Google Books candidate take the row's first slot over a
    usable Audible one, and bulk apply then refused the book as review-only.
