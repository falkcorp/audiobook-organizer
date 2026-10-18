- [ ] **ACTIVITY-CLAMP-OP** There is no way to shrink oversized activity
      summaries on rows written before the write-time cap existed.
      `POST /api/v1/activity/clamp-summaries` (retroactive clamp, optional
      vacuum) was retired in 01-P81g and its code deleted; nothing replaces it.
      It matters for prod disk growth: the 2026-09-07 measurement had the
      activity database at 5.3 GB with summaries holding most of it. If it is
      needed again, add a `maintenance.*` op with an `opmode` dry-run (measure
      first, apply on request) that clamps the oversized summaries in batches
      and then vacuums and truncates the WAL (the deleted `VacuumActivity` did a
      VACUUM, then a PASSIVE-converge-then-TRUNCATE checkpoint; see git history
      before 01-P81g). Run it on the SQLite activity backend only.
