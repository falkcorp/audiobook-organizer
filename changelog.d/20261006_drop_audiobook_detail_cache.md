### Fixed

- **Book detail no longer shows a title from before a background write.**
  `GET /api/v1/audiobooks/:id` was served from a 24-hour per-book cache in
  `AudiobookService` that only the service's own edits cleared. A write by a
  Repairs fixer, the scanner or a metadata apply left the old copy in place,
  so after the 2026-10-06 scan-title revert 5 of 10 spot-checked books still
  showed the scan's title on their detail page although the store and their
  metadata history had the restored one. The cache is removed: every detail
  read goes to the store. Measured on prod, an uncached read is ~31 ms median
  and 48 ms p90, against ~7 ms from the cache. The list cache is unchanged.
