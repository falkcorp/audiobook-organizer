### Fixed

- The memdb warmup reported **Pebble key counts as book counts**. `warmIter`
  returned the number of keys it visited under the `book:` prefix, but that
  prefix is shared with roughly seven secondary-index families
  (`book:path:`, `book:hash:`, `book:versiongroup:`, …) which the row callback
  skips and yet were still counted. Production logged `books=366922` for a
  library of roughly 49,000 books — about 7.5 keys per row. The same inflation
  applied to `authors`, `book_files` and the other shared-prefix tables.
  Warmup now reports rows actually inserted into memdb, and reports keys
  scanned separately under its own name so the two cannot be confused.
- **Retracts a bug that never existed.** Whole-library iterators were measured
  against that inflated number and appeared to be returning 13.3% of the
  library — first the version-group backfill (`scanned=48874`), then the
  organizer (`Fetched 48896 total books`). Both scans were complete. Two
  numerators agreeing said nothing, because they shared one unverified
  denominator.
