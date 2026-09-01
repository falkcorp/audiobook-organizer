- [ ] **TODO-REVIEW-PUSHDOWN** Push the metadata review lane's filters down to
      the server so the lane can stop fetching its whole result set. Today
      `useMetadataLane.ts:492` calls `getCachedReviewResults(0, 0)` — `limit=0`,
      i.e. every reviewable row (5,774 on production) — and paginates
      client-side at `useMetadataLane.ts:752`. That is currently CORRECT and
      must not be "fixed" by simply passing a real limit/offset: the eight
      filter switches, the provider filter, the title regex and the threshold
      all run client-side over the full set, `staleIds`
      (`useMetadataLane.ts:1110`) is documented as spanning the library
      precisely because no page can show it, and candidate grouping spans the
      set too. `GET /audiobooks/metadata/cache/review`
      (`internal/server/handlers/metadata_cache.go:271`) accepts only
      `limit`/`offset` with no filter parameters, so paginating the client today
      would silently confine every filter to one page. The real work is
      server-side: accept the filter/threshold/provider parameters, apply them
      before pagination, and return the stale-id set and group keys as
      whole-set summaries alongside the page. Backend change first, then the
      client. Also worth doing in the same pass:
      `metadata_cache.go:271-284` resolves `GetCachedCandidates` for every
      prepared row on every request, which is only tolerable because
      `limit=0` makes the page the whole set anyway.
