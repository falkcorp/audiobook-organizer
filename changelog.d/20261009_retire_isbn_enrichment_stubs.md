### Changed

- Removed the two retired ISBN-enrichment stub operations, `scheduler.isbn-enrichment` and `maintenance.isbn-enrichment`. They only ever returned a "retired" error; `metafetch.asin-backfill` is the replacement. Both IDs are recorded as retired, so old operation rows still resolve. No permission change.
