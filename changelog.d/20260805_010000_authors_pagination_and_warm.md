<!-- file: changelog.d/20260805_010000_authors_pagination_and_warm.md -->
<!-- version: 1.0.0 -->
<!-- guid: bc832a8d-c9ee-46b3-a8d2-d9e59e588137 -->
<!-- last-edited: 2026-08-05 -->

### Fixed

- **`GET /api/v1/authors` ignored `limit` and `offset` entirely.** Measured against
  production, every request returned all 9,203 authors — about 765 KB — regardless
  of what was asked for:

  ```
  ?limit=5     → 9,203 items, 765 KB
  ?limit=50    → 9,203 items, 765 KB
  ?limit=1000  → 9,203 items, 765 KB
  ```

  Both parameters are now honoured. Paging is applied **after** the cache rather
  than pushed into the query: building the list is the expensive part (it joins book
  and file counts per author), so one cached build serves every page slice, and
  re-querying per page would surrender the cache for nothing.

  `count` deliberately stays the **full** total rather than the slice length — a
  client paging through needs to know how much is left, and reporting the page size
  would make the last page look like the whole set.

  🔑 Omitting `limit` still returns everything. The current UI depends on the unpaged
  response, so paging is strictly opt-in; defaulting to a page size would have
  silently truncated it.

- **The Audiobookshelf Authors tab hung for six seconds after every restart.**
  Building the contributor list is a full-library scan, and the cache starts empty,
  so the first caller paid for it — normally the client's Authors tab. Measured:
  **6,104 ms cold vs 105 ms warm.**

  The cache is now warmed in the background at startup. It waits for the memdb
  warmup first: the cache holds the authors of *visible* books, and building it
  against a half-published memdb would cache a view of a library that does not exist
  yet — and then serve it for the whole TTL. A slow-but-correct warm beats a fast
  wrong one.

  Best-effort by design: a failed warm only means the next request rebuilds, exactly
  as before. It is logged, never returned, and never blocks startup.

  6 tests on the paging, covering limit, offset, an offset past the end, a limit
  larger than the set, unparseable values, and the no-parameters backward-compatibility
  guarantee.
