### Fixed

#### Activity compaction no longer goes silent on the SQLite tier

`maintenance.compact-activity-log` was cancelled by the stuck-op watchdog on
2026-09-13 with `sql_activity: compact range: context canceled`: after the
Pebble tier finished, the SQLite tier sent no progress for over 20 minutes
before its first chunk. Every step before the first chunk is now bounded and
either reports or is a single index seek:

- The range query is a lone `MIN(ts)` per day, which SQLite answers with one
  index seek. Before, it was `MIN(ts), MAX(ts)`: two aggregates defeat SQLite's
  min/max optimization, so the query walked every row below the cutoff with a
  table lookup per row. That measured 1.00s against 0.01s on a 1.5M-row
  fixture. `MAX` was never used. Empty days are skipped instead of costing a
  write transaction each.
- The backfill gate is taken ctx-aware, so a compaction waiting behind a
  backfill batch can be cancelled and logs while it waits.
- The digest item sample now runs on the reader before the day's first chunk,
  as a keyset walk in 5,000-row windows with a liveness stamp per window
  (`registry.TouchLiveness`, the same mechanism tag-backfill uses). Before, it
  was one unbounded statement inside the first chunk's write transaction. The
  sampled items are the same as before.
- The op's progress adapter now forwards per-backend completion events, so
  the handoff between tiers is stamped. Its counter is cumulative across
  backends, and each message names the tier, the day and the rows removed so
  far.

Each chunk still folds its counts into the digest and deletes its rows in one
transaction, so a cancel leaves a digest that counts exactly the rows that are
gone. No schema change.
