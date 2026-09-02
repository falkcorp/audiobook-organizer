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
    answered every query with the unfiltered queue. `useRegroupLane` stands its
    client-side pass down once the server has answered the term, so the lane
    correctly rendered everything it was given. At N=50/100 the larger payload
    landed slowly enough that the assertion caught the *transient* client-side
    narrowing and passed; at N=5 it landed fast, stand-down won, and the
    noise-floor row failed. Every regroup number was therefore the cost of a
    client pass that production does not run.

  Both stubs now filter the way the real endpoints do — the dupes one mirroring
  `ListCandidates`' union in `embedding_store.go` (substring on layer/band,
  **prefix** on the ULID entity ids, joined book fields standing in for the
  resolved id set), the regroup one mirroring `reviewSearchMatches` in
  `review_store.go` (summary/folder_ref/kind/dedup_key/id, then the payload's
  string values, with the same unparseable-falls-back-to-raw rule). The regroup
  stub reads **`q`**, not `search`: the lane's filter field is `search`, but
  `api.ts` puts it on the wire as `q`, and reading the field name matched nothing
  while still returning a valid-looking response.

  Two metric labels were corrected with them: both lanes now report
  `filter (server, 250ms debounce)` rather than describing a client-side,
  undebounced filter neither one still performs.

  The suite now runs 15/15 green. With regroup genuinely server-answered its
  filter cost is flat at ~380 ms from N=5 to N=500 — the debounce floor plus a
  round trip — rather than scaling with the row count.
