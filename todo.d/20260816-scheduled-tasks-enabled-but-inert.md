- [ ] **3 scheduled tasks are ENABLED but can never run.** Startup logs
      `Scheduled task is ENABLED but can NEVER run` for `library_organize`,
      `library_size_refresh` and `metadata_upgrade` — all `interval=0s`,
      `declaresMaintenanceWindow=false`, `inMaintenanceOrder=true`. Pre-existing (15
      occurrences before the 2026-08-16 boot). Each needs either a
      `scheduled.<task>.interval` or `declaresMaintenanceWindow=true`.
      ⚠️ `library_organize` is the trigger for the library-wide relocation from #2479 —
      enabling it starts moving files across the whole library, so decide deliberately.
      See `docs/handoffs/2026-08-16-overnight-silent-failure-fixes.md`.
