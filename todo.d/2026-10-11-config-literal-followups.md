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
