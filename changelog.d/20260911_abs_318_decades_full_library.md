### Fixed

#### ABS `/filterdata` published-decades facet is built from the whole library, not the first 5,000 books (SQ-05)

The Audiobookshelf-compatible `GET /api/libraries/:id/filterdata` derived its `publishedDecades` list from `GetAllBooksCore(5000, 0)` — the same first 5,000 rows in ULID (creation) order on every call — so on a large library any decade whose books were all added later was permanently missing from the client's decade filter, with no truncation signal in the response or the logs. The facet now comes from a new `GetDistinctPublishedYears` store read that walks every live book as a projection (memdb: two pointer reads per row, no row copy; Pebble fallback: the same keyspace scan `GetDistinctLanguages` uses), coalescing `AudiobookReleaseYear` over `PrintYear` and excluding soft-deleted rows exactly as before. The response shape is unchanged; the cost is paid once per cached `/filterdata` build, not per request.
