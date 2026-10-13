- [ ] **BACKUP-SUFFIX-CLEANUP-OPTIN** Owner decision: should the dropped
      `.backup` / `.bak` suffix cleanup come back, and how? The manual
      `maintenance.cleanup-backups` job (retired in 04-P3b) deleted every file
      ending in `.backup` or `.bak` under the library root, with no age test
      (preview by default, no recorded prod run). It was NOT folded into the
      nightly `maintenance.cleanup-old-backups`, because that job runs on a
      schedule and would start age-deleting every `*.bak` file the library
      holds, while it deliberately deletes only the `.bak-<unix>[-N]` names
      `fileops` writes. Option: an opt-in, age-gated, preview-default param on
      the survivor, so an operator can ask for the suffix sweep explicitly.
      Otherwise leave it dropped. Also: after the 2-week soak of
      the op-id retirement, check prod logs for
      `unknown defID "maintenance.cleanup-backups"` and check for
      `operations_v2` rows with that def_id (the alias deprecation metric
      cannot fire for a retired, unaliased ID).
