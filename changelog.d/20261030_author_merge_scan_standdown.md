### Fixed

- `entities.author-merge` (the Authors view's merge) and `ai.author-scan-apply`
  (Apply selected in the AI scans sub-view) now stand the library scanner down
  for the whole merge, the way `dedup.series-merge` already did. A `library.scan`
  running at the same time rewrites `book_authors` on the rows the merge
  repoints and could undo the move. Each op takes the stand-down before its first
  write, renews it once per author, and releases it on every exit. An apply with
  no operation id, or while the scan will not park, is refused before anything is
  written. If the lease is lost part way the run stops before the next author,
  clears the cached author lists, and fails naming the authors it did not reach
  (the AI apply also leaves the result it was on, and the results after it,
  unapplied). A preview of an AI scan apply does not take the stand-down.
