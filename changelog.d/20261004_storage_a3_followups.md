### Changed

#### db-health embeddings come from the census, with error bounds

`embeddings.vector_count` and `embeddings.size_bytes` now come from the census
`emb:v:` family (all entity types, not only book and author) instead of walking
every vector key on each request, and carry `estimated: true`.
`size_bytes` was always 0 before and is now the family's on-disk bytes.
`pebble`, `embeddings`, `metadata_cache` and the `/cache/stats` `metadata_fetch`
entry now also return `error_bound_keys` (`size_error_bound_keys` on
`/cache/stats`) beside each estimated count.

#### Diagnostics DB-health card: "Count expired" button

The expired-entries cell offers a "Count expired" button that calls
`db-health?deep=true`, or shows "TTL off" when the cache TTL is 0.

### Fixed

#### Skip expensive fallback counts after the client disconnects

The full-walk fallback counts in db-health and `/cache/stats` no longer run once
the request context is cancelled, and `/cache/stats` no longer computes a
metadata-fetch size when there is no `metadata_fetch` row to show it on.
`AIScanStore.HealthStats` turns a closed shared DB into an error instead of a
panic.
