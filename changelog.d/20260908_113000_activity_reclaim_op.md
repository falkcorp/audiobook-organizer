### Added

- **New maintenance job: `maintenance.activity-reclaim`.** The Pebble→SQLite
  activity migration only ever COPIED — nothing deleted the Pebble side, so
  after the cutover the whole Pebble activity keyspace (~1.3 GiB on production,
  ~0.78 GiB of it secondary-index keys) stayed in the main database read by
  nobody. This is the delete half. Trigger it from the operations UI; it is
  dry-run unless you pass `dry_run=false`, and it reports a full census — rows
  on each backend, cutover state, how much is eligible — even when it refuses.

### Fixed

- **Activity row counts were silently capped.** `Query`'s total is a pagination
  probe on both backends (Pebble stops at `Offset+Limit+1`, SQLite caps its
  COUNT at `sqlActCountCap`), so anything reading it as "how many rows are
  there" under-reported a large keyspace by orders of magnitude. Adds an exact
  `CountActivity` on both stores for census and retention decisions.
