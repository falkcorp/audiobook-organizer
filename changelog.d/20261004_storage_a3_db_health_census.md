### Changed

#### db-health and /cache/stats read the census instead of scanning

`GET /diagnostics/db-health` no longer iterates every key and decodes every
metadata-fetch-cache row, and `/cache/stats` no longer counts the fetch cache by
scanning it. Both take their numbers from the estimated census. Field meanings
that change:

#### `pebble.key_count` is the census total, `estimated: true`

It was an exact count of live keys from a full iteration. It is now the census
`TotalKeys`: sstable entries minus tombstones, including overwritten versions
not yet compacted, excluding the memtable. It can read higher than the old live
count on a store with many uncompacted rewrites.

#### `metadata_cache.total_entries` is the census family estimate

It was an exact count; it is now the `metadata_fetch_cache:` family estimate,
with `estimated: true`. A backend without a census keeps the exact count and
`estimated: false`.

#### `metadata_cache.expired_entries` is -1 unless `?deep=true`

Counting expired rows decodes every cache row. The default answer is `-1` with
`expired_entries_computed: false`; `?deep=true` counts by streaming the family
in pages of 1000 and decoding only `cached_at`.

#### `ai_scans.size_bytes` measures the `aiscan:` prefix on the shared store

On the shared main store it was the whole database; it is now an estimate of the
`aiscan:` key range. New `ai_scans.size_source` is `store_disk_usage` (owned
store) or `aiscan_prefix_estimate` (shared).

#### `/cache/stats` `metadata_fetch.size` is the census estimate

With `size_estimated: true`. Without a census the exact `CountPrefix` is kept.

### Fixed

#### ScanPrefix and CountPrefix wrapped the upper bound of a prefix ending in 0xff

The bound was built by incrementing the last byte, so a prefix ending in `0xff`
wrapped to `0x00` and matched nothing, and the empty prefix panicked. Both now
use `prefixUpperBound` (an all-`0xff` prefix is unbounded; the empty prefix
covers every key).

### Removed

#### `PebbleStore.KeyCount`

Its only caller was db-health.
