### Changed

#### db-health deep expired count is single-flight, cached and bounded

`GET /diagnostics/db-health?deep=true` now shares one walk between concurrent
callers, runs it detached from the caller with a 2 minute timeout, checks the
request context between pages, and serves a finished count for 60 seconds. A
failed count returns `metadata_cache.expired_entries_error`, and the
Diagnostics card shows "Expired count failed: <reason>". A cache TTL of 0 or
less shows "TTL off". The embeddings count shows `~` with its error bound as a
tooltip.

### Fixed

#### AI-scan health no longer panics on a closed shared DB

`AIScanStore.HealthStats` converts only a closed-pebble panic (from `ListScans`
or the disk-usage estimate) into an error, through the shared
`recoverPebbleClosed` helper, and re-raises any other panic. The earlier
blanket recover around the disk-usage estimate is removed.
