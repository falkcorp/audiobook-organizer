### Fixed

- **The batch `isbn-enrichment` op now acquires ASINs for books that already have
  an ISBN.** Its gate previously treated a book as fully identified once it had
  either ISBN, so a book with an ISBN but no ASIN was skipped and its ASIN was
  never fetched on the batch path (only the per-book path handled this). The gate
  now enriches a book that is missing *either* an ISBN *or* an ASIN, matching the
  per-book enrichment logic. Books that have both identifiers are still skipped.
