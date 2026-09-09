### Changed

- **"Resume Review" no longer downloads the whole review backlog to count it.**
  Clicking it fetched every book with cached metadata candidates — 40,485 rows,
  a 7.35 MB response — read how many there were, then threw all of it away and
  moved to the review screen, which loads its own data anyway. It now asks for
  the count and a single row. The number shown is the same number as before: the
  server reports the size of the whole matching set independently of how many
  rows it sends back.

  This is the other half of the paging fix in the same release. That change made
  the listing able to return a page instead of everything; this one is the caller
  finally asking for one. Note that the wait before the review screen opens is
  dominated by the server's scan of the metadata cache, which this does not
  touch — what improves is the several megabytes that no longer cross the network
  and get parsed in the browser first.
