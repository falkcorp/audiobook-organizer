- [ ] **RESET-TO-DEFAULTS-MISSING-LEAVES** `ResetToDefaults` (internal/config/config.go)
      is a second hand-written `Config{...}` literal and the #3952 review found 131
      leaves missing from it; FactoryReset persists the result, so the stored blob
      gets zero/null for, among others, `search.result_cache.*`,
      `library_counts_cache_min_interval_seconds`, `list_warmer_*`,
      `fp_parallel_workers`, `fingerprint_length_sec`, `telemetry_environment`,
      `backup_compression`, `activity_backend`, `oauth_default_role`, the ABS
      auth/TTL keys and several `scheduled.*.interval` values. Fix: derive it from
      the viper defaults (the same reads InitConfig does) instead of copying values
      by hand, and extend `initconfig_reads_every_key_test.go`'s enumeration to it.
      `log_retention_days` and `enabled_sort_indexes` are already safe there.
- [ ] **LOG-PURGE-UNBOUNDED-BATCH** `PruneOperationLogs` and `PruneSystemActivityLogs`
      (internal/database/pebble_store_activity.go) each delete every expired row in
      ONE Pebble batch and one commit. The purge never ran before 2026-10-11
      (`log_retention_days` was never read), so the first run on an old install
      deletes the whole backlog in a single batch under the op's 30-minute,
      non-cancellable budget. Chunk them like `PruneOperationChanges`
      (`opChangePruneChunk`, commit every 5,000 rows).
