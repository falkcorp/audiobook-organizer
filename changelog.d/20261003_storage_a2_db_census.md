### Added

#### `GET /api/v1/diagnostics/db-census` and the `maintenance.db-census-exact` op: a per-key-family census of the main Pebble store

A new key-family registry (`internal/database/keyfamilies.go`) lists every key
prefix in the main Pebble store with a description and owning file. It is the
single source for the census and, later, for the generated key-schema doc.
Families nest: a parent's figures exclude its registered children, and every
key range that no family covers is reported as `(unregistered)`. The registry
pre-registers the TASK-A5 timeline indexes `opv2:open:` and `opv2:done:`, and
breaks the `_system` user's records under `pref:_system:` out by sub-prefix.

The endpoint (`PermSettingsManage`, the same as db-health) stays cheap. It
flushes the memtable, then reads sstable properties and span bytes and makes
one bounded seek per family range; it reads no values and iterates no family.
It splits each table between the families that hold anything, and that
includes families holding only tombstones or keys under uncompacted range
deletions, which the seek detects from the iterator stats and from the table's
`NumRangeDeletions`. The response reports each family's figures with an
`error_bound_keys`, and the per-family entries and disk bytes add up to the
totals. If a compaction changes the table set mid-read, the read is retried.
The response also carries `last_exact`, the last census the op wrote, with its
timestamp, and `exact_in_progress` while a run is unfinished. The estimated
part is cached for 5 minutes, in a cache bounded to 8 stores. Concurrent
callers share one computation that runs detached from any single caller.

`maintenance.db-census-exact` is a manual, low-priority op. It counts every
family exactly with a keys-only pass, limited to `db_census_exact_read_mb_per_sec`
(default 50 MB/s), so it does not evict the cache the library is served from.
It saves progress after each family and resumes after a restart. It refuses to
start within `db_census_exact_cooldown_hours` (default 6) of the last run
unless `force=true`. The same pass builds the per-book `book_ver:` history
distribution and the retired-book and signal counts. Before this, the only full
count was db-health's `KeyCount`, which iterates all 174M production keys in
about 5 minutes.
