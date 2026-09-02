### Fixed

- **The review-lane performance harness was measuring the wrong thing on two of
  three lanes, and nothing noticed because it is gate-exempt.** `benchmark-review-lanes.spec.ts`
  is excluded from the CI projects by design (`testIgnore: '**/benchmark-*.spec.ts'`),
  so both faults below were invisible until somebody ran it by hand.

  - **dupes: broken outright.** The driver located the search box by its old
    accessible name, "Search this page". That label became plain "Search" when
    the term was pushed server-side — the box no longer searches only the page —
    so the locator resolved to nothing and the run died on a five-minute timeout
    instead of producing numbers.
  - **regroup: passing by luck.** Its stub ignored the search parameter and
    answered every query with the unfiltered queue. `useRegroupLane` narrows on
    the client at the debounce and stands that pass down once the server has
    answered the term, so the lane correctly rendered everything the stub gave
    it — the narrowed DOM existed only until the response landed. The assertion
    polls on Playwright's default schedule (100/250/500/1000 ms), so exactly one
    sample could catch that transient; N=50/100 happened to, N=5 missed it and
    failed outright. The row counts were a race, not a measurement.

  Both stubs now filter the way the real endpoints do — the dupes one mirroring
  `ListCandidates`' union in `embedding_store.go` (substring on layer/band,
  **prefix** on the ULID entity ids, joined book fields standing in for the
  resolved id set), the regroup one mirroring `reviewSearchMatches` in
  `review_store.go` (summary/folder_ref/kind/dedup_key/id, then the payload's
  string values walked recursively, with the same unparseable-falls-back-to-raw
  rule). The regroup stub reads **`q`**, not `search`: the lane's filter field is `search`, but
  `api.ts` puts it on the wire as `q`, and reading the field name matched nothing
  while still returning a valid-looking response.

  Two metric labels were corrected with them: both lanes now report
  `filter (server, 250ms debounce)` rather than describing a client-side,
  undebounced filter neither one still performs.

  Both filter drivers now also assert, after the timed block, that the stub was
  asked the term and answered with one row — so a future parameter rename fails
  loudly instead of quietly reverting to the race.

  The suite runs 15/15 green. Read the filter numbers for what they are: both
  lanes narrow the DOM off their client pass at the 250 ms debounce, and the
  poll catches it at its next sample, so ~380 ms is debounce plus poll
  quantisation, not a server round trip. Flat from N=5 to N=500 on regroup is
  the tell — a real round trip plus a 500-row commit could not be flat.
