### Fixed

- **Embedding hydration no longer discards vectors silently.** `HydrateChromem`
  read 39,658 book embedding rows on production and put 17,706 into the vector
  index; the other 21,952 vanished with only 1 of them reported anywhere,
  because three of the four book skip paths incremented no counter and logged
  nothing. Every skip path now has a named bucket — empty vector, stale model,
  orphaned (the book no longer exists), lookup error, non-primary version, and
  a failed write into the index — and one summary line reports all of them, so
  the gap between the vector store's `truth_count` and its `graph_count` is
  fully explained instead of being a mystery. The line warns rather than
  informs whenever a bucket wants a human, and reports whether the run was cut
  short so a partial hydrate cannot be misread as a clean one.
- **The hydrated count now means "reached the index", not "we tried".** The
  helpers that write into the vector store were best-effort and swallowed their
  failure, so a rejected write still counted as hydrated. They now report it and
  it lands in its own bucket.
- **Orphan rows are counted separately from lookup faults.** The old code
  discarded the store's error, so a read fault that drops a *live* book out of
  deduplication was indistinguishable from a row whose book is genuinely gone.
  The orphan count is the cleanup signal for a book-side counterpart to
  `dedup.cleanup-orphan-author-embeddings`; no rows are deleted here.
- **Corrupt embedding records are no longer invisible.** The store dropped any
  record that failed to parse without counting it, which also made its row count
  disagree, permanently and silently, with the raw key count the vector store
  reports as its `truth_count` — the two numbers operators are told to compare.
  The new `ListByTypeCounted` reports the difference and hydration surfaces it.
