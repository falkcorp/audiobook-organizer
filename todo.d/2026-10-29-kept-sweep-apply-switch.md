- [ ] **TODO-KEPT-SWEEP-APPLY** the kept-file sweep (`versions.SweepKeptFiles`, a step of
      `maintenance.trash-cleanup`) ships report-only. Read a night or two of its result
      (`kept_sweep_would_remove_files`, `kept_sweep_would_drop_files`: version, book, path and
      the reason each file had been kept), check the list, then set `kept_sweep_apply_enabled`
      (env `KEPT_SWEEP_APPLY_ENABLED`) to true. Until then it removes nothing; `PurgeKept`
      only exists since 2026-10-28, so the first list covers purges since then. Decide
      afterwards whether the switch should be removed and the sweep always applying.
