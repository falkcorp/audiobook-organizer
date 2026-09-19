### Added

#### Pebble activity log — indexed source, type and level filters

Activity filters other than operation and book used to walk only the newest
20,000 entries of the Pebble activity log, so an older match was never
examined and the answer looked complete. The Pebble store now keeps
`act:src:`, `act:typ:` and `act:lvl:` secondary indexes (keyed
`<escaped value>:<tier>:<nanos>:<ulid>`), written and deleted in the same batch
as the row by Record, RecordBatch, Summarize, Prune, CompactByDay and
WipeAllActivity, and cleaned by the existing index repair. Query now plans
operation → book → the smallest filter index, probes the other indexed filters
by key, and applies the rest per row, with the same order, paging and totals as
before. On a 1M-row store: level 28.7 ms (20 of 50 rows, truncated) → 3.6 ms
(50 rows); source 13.0 → 2.1 ms; type 6.2 → 2.1 ms; source+level page 3
31.8 ms (0 rows, truncated) → 24.2 ms (50 rows).

The indexes are only used after the new
`maintenance.activity-filter-index-backfill` op (resumable, parallel, shares
the activity-compaction concurrency key) has indexed the existing rows; until
then filters behave as before. Run it once after deploying, before switching
`ACTIVITY_BACKEND` to `pebble`.

#### `GET /activity` — `partial` flag

A search that hits the scan budget (for example a text search) now returns
`"partial": true`, and the Activity page says older matches were not searched
instead of presenting a short or empty page as the whole answer.

#### Filter-index safety: forced rebuilds, rollbacks, deep offsets

A forced backfill (`{"force": true}`) now turns indexed filtering off before
it writes anything. Each write also records the newest row timestamp the
indexing build has written. At boot, rows newer than that mark are checked:
rows written without index keys by a rolled-back build turn indexed filtering
off, get indexed, and then turn it back on. Filtered `/activity` requests with
an offset above 100,000 return 400 instead of reading that many rows. The
backfill now closes its iterator before each commit, and
`maintenance.activity-reclaim` shares the activity-maintenance concurrency key
with it.
