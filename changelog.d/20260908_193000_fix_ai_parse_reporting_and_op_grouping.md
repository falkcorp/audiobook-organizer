### Fixed

- **A failed AI filename-parsing run reported itself as completed, with a full
  green progress bar.** The Activity page showed dozens of consecutive "AI
  Filename Parsing" rows, each reading `0/5 book(s) parsed in 0/1 batches; 1
  batch failure(s), 0 save failure(s)`, every one of them marked COMPLETED. The
  operation failed only on `summary.Aborted()`, which means
  `AbortedPermanent || AbortedThreshold` — and a single-batch run whose only
  batch fails trips neither, because it did not stop *early*, it stopped on
  schedule having failed. A run is now failed if any batch failed or any save
  was lost; a healthy library where every candidate was already filled in by
  another path still parses nothing and still reports success.

- **A failed batch said nothing about what failed.** The filenames in the batch
  and the backend's error both existed at the failure site in
  `internal/scanner/ai_batch_phase.go` and were logged with `log.Warn`, which
  `LoggerFromReporter` does not forward — so the operation record carried a
  count and no way to act on it. Both are now captured onto the summary, the
  first cause appears on the row itself, and one line per failure goes into the
  operation record. The lists are capped (5 batch failures, 10 filenames each,
  10 save failures) because these are written into the activity store, and a
  truncated list now says how many it dropped instead of reading as the total.

- **A run that failed every batch could report zero batch failures.** The
  failure counter was incremented *below* the non-retryable-error branch, so the
  worst case — a revoked key, an exhausted quota, every batch dead — returned
  `BatchesFailed == 0` and printed "0 batch failure(s)". The abort threshold is
  unchanged.

- **Terminal operations rendered a full progress bar regardless of progress.** A
  row reading `0 / 4 (0.00%)` drew a solid green bar directly above its own
  caption. The bar was hardcoded to 100% for anything that had ended, conflating
  "reached a terminal state" with "finished the work". It now shows the real
  fraction when there is a denominator.

### Changed

- **Consecutive runs of the same operation now collapse into one row.** A scan
  can queue dozens of identical background operations, and the Activity page and
  the notification bell listed every one. Runs of the same operation type and
  status that are less than 2 minutes apart (and span no more than 30 minutes)
  are now folded into a single collapsible row showing how many runs it stands
  for; expanding it shows every member. Nothing is stored — the grouping is
  derived each time the list is read, so it cannot go stale, cannot be split
  across a page boundary, and leaves no rows behind to clean up.

  Section headings and the bell's section counts are now taken from the
  UNGROUPED set. A heading is a census of operations, and a group row is not an
  operation — counting rendered rows would print `Failed (1)` over the twelve
  failed runs that prompted this change, and flip to `Failed (13)` on expand.
  Each section is one predicate applied twice, to the rows it renders and to the
  raw ops it counts, so the two cannot drift.
