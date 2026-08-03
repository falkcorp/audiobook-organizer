<!-- file: changelog.d/20260803_020000_authors_document_cache.md -->
<!-- version: 1.0.0 -->
<!-- guid: f73c1f6e-cf17-45f4-bc67-bb711ba9e436 -->
<!-- last-edited: 2026-08-03 -->

### Fixed

- **Jumping to a letter in the Authors tab took ~37 seconds.** Per-request latency was
  never the real metric — request COUNT was. AudioBooth pages authors 100 at a time and
  its jump-to-letter feature keeps loading pages until the target letter appears
  (`AuthorsPageModel`: `itemsPerPage = 100`,
  `hasMorePages = currentPage * itemsPerPage < total`). With ~9,200 authors, reaching
  "Z" is ~93 consecutive requests.

  Each of those rebuilt the whole author list, and `GetAllAuthorBookCounts` is by its
  own description a "Full Pebble book scan combined with junction table scan" — all
  44,888 books walked, per page. 93 full library scans.

  The built list is now cached for 5 minutes, so pages 2..93 are slice arithmetic. The
  same cache serves the home screen's author shelf, which rebuilt it on every refresh
  too. Built outside the lock, so a burst of page requests does not serialize behind
  one rebuild.
