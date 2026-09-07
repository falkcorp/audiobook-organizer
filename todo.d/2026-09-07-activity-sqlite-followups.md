## Activity SQLite backend — follow-ups after the cutover

The SQLite activity backend + Pebble→SQLite migration landed (backend-agnostic
store, bounded `CompactByDay`, dual-write + parity-gated flip). Remaining work:

- [ ] **🔴 BLOCKER — rewrite the Pebble→SQLite backfill to stream.**
  `BackfillPebbleActivityToSQL` calls `scanTierKVs(ctx, tier, nil, nil)`, which
  materializes a whole activity tier into memory before the first insert. On prod
  (2026-09-07) this drove RSS to ~30 G on the `change` tier and the kernel
  OOM-killed the service in a ~14-min restart loop. Rewrite copy AND parity to
  stream from a Pebble iterator in bounded `sqlBackfillBatch` windows — the parity
  pass must re-iterate Pebble independently (it can't reuse the copy slice), which
  is strictly stronger. **Until this lands, SQLite is disabled on prod via
  `ACTIVITY_BACKEND=pebble` in `deploy/local.conf`** (rollback lever wired in
  #3088). Re-enable only after a bounded-memory backfill is verified.
- [ ] **Audit `scanTierKVs` callers for the same OOM shape.** `Summarize` /
  `CompactByDay` on the Pebble side may also call the full-tier materializer; if so
  that is a pre-existing hazard independent of the migration.
- [ ] **Scheduled auto-compaction toggle.** A `ScheduledTaskConfig` (like
  `reconcile`/`ai_dedup_batch`) that, when enabled, compacts the *previous day*
  on a schedule. Trivial now that `CompactByDay` is bounded on SQLite. This was
  the original user ask ("a setting we can turn on that runs autocompaction as a
  scheduled task that compacts the previous day").
- [ ] **Make the "Compact after N days" button async.** The handler
  (`internal/server/handlers/activity.go` `CompactActivity`) still runs
  `CompactByDay` synchronously in the HTTP request. SQLite makes it bounded/fast,
  but enqueuing an op is the correct shape and removes the browser-timeout
  coupling entirely.
- [ ] **Retire the Pebble activity path** once SQLite reads have soaked in prod:
  stop dual-writing to Pebble and range-delete the `act:` keyspace to reclaim the
  ~1.3 GiB it holds. Separate PR; do NOT do it until the flip is verified in prod.
- [ ] **MySQL/Postgres dialects.** The `sqlDialect` seam is built; adding a
  networked backend is a dialect + driver + DSN/credentials decision.
