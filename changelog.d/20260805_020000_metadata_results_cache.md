<!-- file: changelog.d/20260805_020000_metadata_results_cache.md -->
<!-- version: 1.0.0 -->
<!-- guid: 65b9ee5d-b3df-43ed-95a3-b393bb0532a7 -->
<!-- last-edited: 2026-08-05 -->

### Fixed

- **`GET /api/v1/library/metadata-results` took 21.9 seconds — for three rows.**
  Measured against production: `?limit=3` out of 36,805 results, 21,912 ms.

  The page slice was applied *after* the build, and the build re-ran on every
  request regardless of the page asked for:

  ```
  GetRecentOperations(5000)
    → a SEPARATE GetOperationResults(op.ID) per metadata_candidate_fetch op
    → folded into a latest-per-book map
  ```

  So `?limit=3` cost exactly what `?limit=5000` did. That made choosing metadata
  matches impractical — every interaction paid twenty seconds.

  The build is now memoised for 60 seconds, mirroring the ABS contributor cache for
  the same reason: assembling the set is the expensive part, and every page is a
  free slice of it. The rebuild happens **outside the lock**, because holding it
  across a multi-second build would serialise every concurrent request behind one
  rebuild — exactly the access pattern a UI paging through results produces.

  Applying, rejecting and un-rejecting a candidate all invalidate the entry. A
  status change must not leave the list offering a candidate the user just acted
  on; that is the one kind of staleness that actively misleads rather than merely
  lags.

  5 tests, including that a fresh entry is served without touching the store at all
  (a nil store proves no rebuild), that invalidation clears it, and that the TTL
  stays inside a band short enough to feel live.
