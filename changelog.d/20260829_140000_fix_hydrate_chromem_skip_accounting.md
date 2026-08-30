### Fixed

- **Embedding hydration no longer discards vectors silently.** `HydrateChromem`
  read 39,658 book embedding rows on production and put 17,706 into the vector
  index; the other 21,952 vanished with only 1 of them reported anywhere,
  because three of the four book skip paths incremented no counter and logged
  nothing. Every skip path now has a named bucket — empty vector, stale model,
  orphaned (the book no longer exists), lookup error, and non-primary version —
  and one summary line reports all of them, so the gap between the ANN store's
  `truth_count` and its `graph_count` is fully explained instead of being a
  mystery. Orphan rows are counted separately from lookup errors, which the old
  code conflated by discarding `GetBookByID`'s error: an orphan is a correct
  permanent skip, a lookup error means a live book fell out of dedup. The
  orphan count is the cleanup signal for a book-side counterpart to
  `dedup.cleanup-orphan-author-embeddings`; no rows are deleted here.
