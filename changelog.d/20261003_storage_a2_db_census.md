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
flushes the memtable when it holds at least 1 MiB, and at most once per store
per minute. It then reads sstable properties and span bytes, and makes one
bounded seek per family range, but only for ranges with at most 1 MiB of data;
larger ranges count as non-empty without a seek. It reads no values and
iterates no family.
It splits each table between the families that hold anything, and that
includes families holding only tombstones or keys under uncompacted range
deletions, which the seek detects from the iterator stats and from the table's
`NumRangeDeletions`. The response reports each family's figures with an
`error_bound_keys`, and the per-family entries and disk bytes add up to the
totals. If a compaction changes the table set mid-read, the read is retried.
The response also carries `last_exact`, the last census the op wrote, with its
timestamp, and `exact_in_progress` while a run is unfinished. The estimated
part is cached for 5 minutes (15 seconds while memdb is still warming), in a cache bounded to 8 stores. Concurrent
callers share one computation that runs detached from any single caller.

`maintenance.db-census-exact` is a manual, low-priority op. It counts every
family exactly with a keys-only pass, limited to `db_census_exact_read_mb_per_sec`
(default 50 MB/s). The budget is charged on everything the iterator steps
over, including tombstones and shadowed versions, so the op does not evict the
cache the library is served from. The op saves its position every 30 seconds,
inside a family too, and resumes from there after a restart. A cancel stops it
at the next check and publishes nothing. It refuses to start within
`db_census_exact_cooldown_hours` (default 6) of the last run unless
`force=true`. `force=true` and `restart=true` both discard saved progress. The same pass builds the per-book `book_ver:` history
distribution and the retired-book and signal counts. Before this, the only full
count was db-health's `KeyCount`, which iterates all 174M production keys in
about 5 minutes.
