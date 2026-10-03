### Changed

#### Review page index is served from an incrementally invalidated snapshot

`GET /api/v1/audiobooks/metadata/cache/review?view=index&all=true` took 8.3 s on
a quiet production server and 40–105 s while an apply op ran, for two reasons.
Every applied book deleted its metadata-cache row, which moved the cache's
write counter, so the review snapshot was rebuilt in FULL every 45 s (56k rows,
every cache entry decoded twice, ~1 GB allocated per build) and five parallel
index requests sat waiting on one cold build. And every request re-read every
row's book (`GetBooksByIDs` over all 56k ids: 56k Pebble point reads plus a
JSON decode each) to overlay live review status.

The metadata-cache write counter is now a generation whose every bump names the
book written (`database.MetadataCacheChangedSince`, mirroring
`BooksChangedSince`; `Reset` records a bump nothing can list past). The
snapshot keeps every row's book and its library generation, and a rebuild asks
both change logs what moved since the previous snapshot, re-reads only those
rows through an id-driven loader (sharing the whole-cache loader's chunk
worker), carries every other row over, handles row/orphan/gone transitions,
re-sorts, and inherits the last full build's `builtAt` so the 30-minute age
net still forces a full read of what neither log sees. Each request reads
only the books `BooksChangedSince` names (a deleted one is orphaned for that
request); when the log cannot answer, or more than 2,000 books changed, it
reads every book as before and asks the cache for a rebuild right away. The
slow-listing WARN now carries `snapshot_ms` / `overlay_ms` / `prepare_ms` /
`encode_ms`, logged after the response is written.
