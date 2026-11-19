- [ ] **TODO-MERGE-CLEANUP-REPLAN-GUARDS** The `maintenance.merge-cleanup` Replan has no
      short-index guard: Plan refuses a library whose in-memory index is short
      (`mcListingShort`), but Replan builds its own run and reads books one at a time, so a
      short index reads a live book as missing, and the dead-end index then skips a mate
      that reads nil, which lifts a `dead_end_shared` hold. It also lists the whole library
      and walks every merged chain once per Replan call. Unreachable while the fixer is
      `PlanOnly`. The apply PR must hold one run for the whole session (the Replan
      docstring already says so) and apply the same listing-short check to it before any
      row is written.
