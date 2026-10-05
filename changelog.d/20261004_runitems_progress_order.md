### Fixed

- Parallel operations no longer end on a stale progress line. `registry.RunItems`
  now counts each finished item, renders its label and delivers the progress
  update as one serialized step, so updates reach the reporter in completion
  order. Before, a worker could be descheduled between rendering its label and
  delivering it, and its older update landed after a newer one. The reporter
  keeps the last update it receives, so the progress bar could step backwards
  and the final per-item label could undercount. chapters-backfill reported
  `eligible=11` for a run that persisted all 12, which is what made
  `TestChaptersBackfill_ProgressLabelReportsEligibleCount` flaky. A new
  deterministic test, `TestRunItems_ParallelLastProgressUpdateSeesAllWork`,
  forces that interleaving.
