### Fixed

- `backfill-file-hashes` can now finish a library too big for one run's 4 hour
  limit. A prod dry run over 791,451 rows was cancelled at 307,328 by
  "timeout: run exceeded 4h0m0s" and could never have completed, because a
  timed-out run is final and a retry is a new operation whose progress report
  (named after the operation id) starts from row zero. The job now watches its
  own deadline and, once a chunk is safely on disk and less than 5 minutes (or
  twice its slowest chunk) of the limit is left, hands itself back with the new
  `maintenance.ErrContinueInPlace` (`registry.ErrContinueInPlace`). The registry
  records it as a pause and restarts the same operation under the same id, from
  its report, with a fresh 4 hour limit. A run that only pauses when it has
  made progress in that attempt cannot restart forever. The limit still kills a
  stuck run: a deadline that fires is still `timed_out`, a watchdog kill is
  still `canceled`, and the sentinel is honoured only for an in-process
  `ResumeRestart` run whose context is still live (anything else is an ordinary
  failure carrying the error text).
- The maintenance job wrapper no longer writes an activity "done" summary when a
  run pauses this way; the summary comes when the operation really ends.
