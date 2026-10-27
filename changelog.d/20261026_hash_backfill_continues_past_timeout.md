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
  its report, with a fresh 4 hour limit. The registry honours the pause only when
  that attempt reported progress past where it started; a run that returns the
  sentinel without progress is recorded as failed with a message saying so,
  instead of being restarted for ever. The limit still kills a
  stuck run: a deadline that fires is still `timed_out`, a watchdog kill is
  still `canceled`, and the sentinel is honoured only for an in-process
  `ResumeRestart` run whose context is still live (anything else is an ordinary
  failure carrying the error text).
- The maintenance job wrapper no longer writes an activity "done" summary when a
  run pauses this way; the summary comes when the operation really ends.
- A pause that continues in place is no longer announced as an ending: no
  `op.terminal` event (so the operations bell and the legacy row keep showing the
  op as running), no "operation finished" log line, and no failure told to
  dependents. The old attempt's progress writer is now finished before the row is
  re-queued, so it can no longer overwrite the resumed attempt's progress. The
  summary is left out only when the registry really restarts the run.
- A resumed `backfill-file-hashes` attempt now reports the position the earlier
  attempt stopped at instead of counting from zero, so the progress bar no longer
  falls back to the start at a pause and `ops.items` does not count the finished
  rows again. A row counted busy (changed while its file was hashed) is retried by
  the next operation, not by the next attempt of the same one.
