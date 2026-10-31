### Fixed

- Factory reset and system reset (`config.ResetToDefaults`) no longer persist zero values for
  the 131 settings its hand-written literal had left out (`search.result_cache.*`,
  `fp_parallel_workers`, `fingerprint_length_sec`, `oauth_default_role`, `backup_compression`,
  `activity_backend`, `telemetry_environment`, the `list_warmer_*` and library-counts cache
  keys, the ABS auth/TTL keys, every `scheduled.*.interval`, and more). The reset now builds its
  Config from the same registered viper defaults and the same reads as `InitConfig`, on a
  private viper with no config file and no environment, so the literal is deleted and a new
  default reaches the reset by construction.
