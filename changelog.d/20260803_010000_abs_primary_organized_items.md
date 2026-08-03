<!-- file: changelog.d/20260803_010000_abs_primary_organized_items.md -->
<!-- version: 1.0.0 -->
<!-- guid: 92b1c34d-e852-4ac7-9971-5fa01eb0f75a -->
<!-- last-edited: 2026-08-03 -->

### Fixed

- **The app listed 44,888 books where the library holds ~16,000, and every page took
  1.3-3.0 s.** `GET /api/libraries/:id/items` counted and listed EVERY book row. The
  app's own counts cache reports the real split — `total_books=44888`,
  `organized_books=16491`, `unorganized_books=23928` — so roughly 28,000 raw imports,
  iTunes-tree copies and alternate versions were being served as library items.

  The item list now serves **primary versions that are organized into the library**,
  excluding quarantined files, using the filtered store calls that already existed.

- **Latency was a fixed full-library scan, not paging.** It was flat across pages
  (page 0 = 2.01 s, page 9 = 1.29 s) because `CountAllBooks` iterates every `book:`
  key **and `json.Unmarshal`s every book** purely to count them — 44,888 decodes per
  request. The filtered count is now cached for 60 s, mirroring `CountPrimaryBooks`'s
  existing TTL cache (PR #2021) rather than adding a second caching style. Reporting
  the unfiltered total also made the client page forever into empty results, since it
  uses `total` to decide whether another page exists.

- **`sort` was silently ignored.** The client always sends
  `sort=media.metadata.title` and the handler never read it, so the library was never
  actually title-sorted. Now honoured via the sorted title index (O(offset+limit)),
  with `?desc=1` reversing it.

  No progress is at risk: §1.8.1 governs `mediaProgress`, a separate payload built
  from the user's own position rows. A book that leaves the item list still appears in
  `/api/me`, so the client deletes nothing.
