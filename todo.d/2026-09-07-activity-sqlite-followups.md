## Activity SQLite backend — follow-ups after the cutover

The SQLite activity backend + Pebble→SQLite migration landed (backend-agnostic
store, bounded `CompactByDay`, dual-write + parity-gated flip). Remaining work:

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
