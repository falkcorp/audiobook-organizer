- [ ] **Legacy operation rows never leave "pending" — the ops UI shows
      finished jobs as running for hours.** Twice on 2026-08-14 this misled
      the operator: the composer scan showed progress 0 while 3h into real
      work, and the E02 chapters dry-run showed as an active 1.5-hour task
      when it had finished at 17:57 with logged results. A
      `GET /api/v1/operations?limit=20` dump shows EVERY maintenance-job row
      of the day stuck at status "pending" — including `fix-file-modes` and
      `normalize-primary-flags`, which completed with journaled summaries.
      The v2 registry rows complete correctly; it is the LEGACY op row
      (`maintenance:<job>` type, created for jobs dispatched via
      `maintenance.job`) whose status/progress is write-only after creation.
      Fix: on v2 op completion, propagate terminal status (+ final
      progress/message) onto the paired LegacyOpID row; backfill-repair the
      day's stuck rows; and the ops UI should render v2 state when a legacy
      row has a live v2 twin. Note the C510 opstate sweep treats unknown
      statuses as KEEP — stuck-pending rows also pin their opstate blobs
      forever, so this defect quietly defeats that retention too.
