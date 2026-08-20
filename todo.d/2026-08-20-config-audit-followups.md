- [ ] **CFG-AUDIT** Triage the findings in
      `docs/audits/2026-08-20-config-option-audit.md` (full config-option
      inventory + grep-verified usage/naming/default audit, 565 options). At
      minimum decide on: (1) `EnableRateLimit=false` not actually disabling
      rate limiting — only `APIRateLimitPerMinute > 0` gates it; (2)
      `AuthRateLimitPerMinute` is fully wired but never enforced anywhere; (3)
      `APIRateLimitPerMinute` default drift between the fresh-install viper
      default (0/unlimited) and `ResetToDefaults()`/`.env.example` (100); (4)
      `ai_backend.local_base_url` defaulting to a hardcoded developer LAN IP,
      which silently routes fresh installs into local-LLM mode; (5)
      `Config.ChapterConsolidationThresholdMin` being omitted from
      `ResetToDefaults()`, so a factory reset silently disables chapter
      consolidation instead of restoring the intended default of 10; (6)
      whether to delete the fully inert `--enable-sqlite3-i-know-the-risks`
      flag now that the SQLite backend is gone; (7) whether to wire up or
      remove the two entirely-unenforced Settings-UI subsystems (Storage
      Quotas, Memory Limits) and the ~10 other dead Settings-page toggles
      (`create_backups`, `verify_after_write`, `AutoFetchMetadata`,
      `EmbedCoverArt`, etc.) listed in the report so users stop being able to
      flip a switch that does nothing.
