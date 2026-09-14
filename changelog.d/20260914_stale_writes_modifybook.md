### Fixed

- Four background writers no longer revert fields another writer saved while
  they were working. Each of them read a book (or its file rows), did slow work,
  then wrote the whole stale row back:
  - ISBN/ASIN enrichment (`internal/metafetch/isbn.go`) now fills the identifier
    inside `ModifyBook` on a fresh copy, and leaves one that another writer
    filled during the provider search alone.
  - The queued AI parse (`internal/scanner/ai_parse_async.go`) re-applies its
    gap-fills onto a fresh copy inside `ModifyBook`; a gap filled meanwhile
    (narrator, publisher, series, year, author, title) is kept.
  - The scanner's organizer-ID relink (`internal/scanner/scanner.go`) sets only
    `FilePath`, inside `ModifyBook`. A failed lookup is now logged instead of
    being dropped silently.
  - Segment-title generation (`internal/metafetch/service_writeback.go`) writes
    track, track count and title through `PatchBookFileFields` (which gains
    `Title` and `TrackCount`), pinned to the track number it read, instead of a
    whole-row `UpdateBookFile` that could revert e.g. a file's `Duration`.
