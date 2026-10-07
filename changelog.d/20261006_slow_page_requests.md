### Fixed

- **Library "Purge Deleted (N)" no longer takes 30 s to 12 min.** The trash count
  never reached its fast path in production: the capability lookup was a bare
  type assertion, which the server's search-indexing store decorator hides. So
  every `GET /audiobooks/soft-deleted` paged the whole trash 1,000 rows at a time,
  copying and sorting all 48k trashed books on each of 49 pages. The lookup now
  resolves through decorators.
  - The memdb trash listing sorts pointers and copies only the requested page.
  - The count refuses a memdb known to be short and falls through to Pebble,
    the same way the listing does, so the two never disagree.

### Changed

- **`GET /system/status` reads a recent window of operations, not all
  history.**
  - Before: the Recent Operations panel asked for every operation ever written
    to show five, so each status call decoded the whole opv2 table (5.5–6 s on
    production, growing daily).
  - Now: `database.ListRecentOperationsV2` reads a 1-hour window and doubles it
    until the result provably equals the all-history answer. On a
    50k-operation table this took 279 ms → 0.47 ms.
- **`GET /audiobooks/metadata/cached` and every other `ListMetadataCacheKeys`
  caller are served from an in-process summary index.**
  - The index is built once and then kept current through the metadata-cache
    change log, so only rows written since the last call are re-read.
  - On 40k rows: 1.0 s for the cold scan, 8 ms for a warm call after one write.
  - The listing reads each book's title and review status from memdb through
    the new `BookListingFieldsReader` capability, instead of a full Pebble book
    read per cached row.
  - It builds response rows only for the requested page.
- **Identical library-list cache misses share one build.** After a restart,
  14 identical `GET /audiobooks` requests used to build the same page in
  parallel, taking 44–160 s each.
  - The shared build is cancelled when the last caller waiting on it
    disconnects, and it has a hard 5-minute ceiling.
  - A caller who leaves gets 499.
  - Errors reach only the callers who waited and are never cached.
