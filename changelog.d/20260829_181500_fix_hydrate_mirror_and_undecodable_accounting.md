### Fixed

- **The hydrated count now means "reached the index", not "we tried".** The
  helpers that write a vector into the ANN store were best-effort and swallowed
  their `Upsert` failure, so a rejected write still incremented the hydrated
  count and the summary line claimed a row was in the graph when it was not.
  They now report the failure and it lands in its own `books_mirror_error` /
  `authors_mirror_error` bucket. The nil-book, empty-author-id and
  empty-vector early returns were the same lie surviving in a corner — a row
  that hit one of them was counted as hydrated without ever reaching the
  graph — so those are reported too.
- **Corrupt embedding records are no longer invisible.** The store dropped any
  record that failed to parse without counting it, which made its row count
  disagree — permanently and silently — with the raw key count the ANN store
  reports as its `truth_count`. Those are the exact two numbers operators are
  told to compare. The new `ListByTypeCounted` reports the difference and
  hydration surfaces it as `book_rows_undecodable` / `author_rows_undecodable`;
  a corrupt row is a live book with no route into deduplication.
- **A partial hydrate can no longer be misread as a clean one.** A run cut
  short by its context, or one whose row listing failed, used to return with no
  summary line emitted at all — so the 30-minute timeout over ~39K rows, the
  run an operator most wants to inspect, was the one with no bucket
  visibility. The accounting is now logged on those paths and flagged
  `incomplete`, and a nonzero unaccounted total raises the line's severity at
  runtime rather than being checked only by a unit test: a future skip path
  added with no counter is exactly the defect this accounting exists to make
  visible, and no test can see a skip path that does not exist yet. The line
  also reports `model_check_active`, because `embeddingModelMatches` returns
  true unconditionally when no embed client is wired, which makes the
  stale-model buckets structurally zero and otherwise unreadable.
