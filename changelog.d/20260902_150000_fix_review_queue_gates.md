### Fixed

#### Review queue: a dismissed duplicate can no longer be merged, decisions no longer corrupt the status index, and replay sees every approved item

Three review-queue bugs found by the 2026-09-02 dedup/review bug hunt (F1, F3,
F4), each with a test that fails when its fix is reverted:

- **Dismissed dedup candidates no longer nominate a merge target.** The
  `duplicate_of` apply asked the dedup track for candidates in *every* status,
  so a pair a human had dismissed ("these are not the same book") still named
  the canonical book and the folder was merged over the dismissal. Nomination
  now runs through an allow-list of live statuses (`pending`) queried per
  status, plus a veto on every terminal status that is re-applied in Go on
  every returned row, so even a lister that ignores the status argument cannot
  promote a human's "no" into a merge. The veto is the store's own definition
  of a verdict (`database.IsTerminalCandidateStatus`: `dismissed`, `merged`)
  rather than a hand-copied list — the first draft of this fix named
  `rejected` / `separate`, two statuses no writer has ever produced, and would
  have been inert for them. A user would have seen a folder they had explicitly
  kept apart quietly disappear into another book.
- **`SetReviewItemDecision` now holds `reviewMu`.** It moved the
  `review_item:status:*` index row without the mutex its sibling writers take,
  so two concurrent decisions on one hold could both delete the old row and
  each write their own — the record holding one status while the index listed
  it under two. The queue badge (`CountReviewItems`, which reads only the
  index) then counted a hold under a status it no longer had. A new invariant
  test races five deciders over overlapping items and asserts every index row
  names an item stored under that status. For rows already damaged, a new
  `maintenance.review-status-index-repair` op rebuilds the index from the
  records — report-only by default, `{"apply": true}` to write, reporting
  `stale_index_entries_removed` / `missing_index_entries_added` alongside the
  `*_found` counts. Operators should run it once (report, then apply) after
  deploying this release.
- **Two maintenance store accessors were inert in production.** The server hands
  maintenance ops their store capabilities through `FileProvenanceStore()` and
  the new `ReviewStatusIndexStore()`, both of which used a bare type assertion
  on the server's store. In production `Start()` wraps that store in the Bleve
  search decorator, which exposes none of the capability methods, so the
  assertion returned nil on exactly the deployment that matters:
  `maintenance.file-provenance-capture` has reported "provenance store not
  initialized" on every production run since it shipped on 2026-08-21, and the
  new repair op would have reported "not supported" the same way. Both now
  resolve through `database.AsCapability`, which walks the decorator chain, and
  a test installs the decorator the way `Start()` does and asserts both
  accessors resolve through it.
- **Replay-approved now pages through every approved hold.** It passed
  `Limit: 0` believing it meant unbounded; the store treats `Limit <= 0` as a
  default page of 50. A queue with 300 approved holds replayed 50, reported
  `approved_total: 50`, and read as finished while 250 decisions stayed
  approved forever. Replay now collects the full approved set by
  `Offset`/`Limit` paging (page size 100) before applying, de-duplicated by ID,
  and `approved_total` is the store's own count. `GET /review/items` refuses
  `limit <= 0`, `limit > 1000`, and a non-integer `limit` with
  `400 REVIEW_LIST_LIMIT_INVALID` instead of silently serving 50 rows, and a
  negative or non-integer `offset` with `400 REVIEW_LIST_OFFSET_INVALID`
  instead of silently serving page one. Because replay's safety cap
  (`refuseIfOverCap`) now sees the true approved count instead of a page of at
  most 50, a queue with more approved holds than the cap is newly refused where
  it used to be silently under-replayed; pass a request `limit` under the cap
  (the dry run reports it) to replay in batches.
