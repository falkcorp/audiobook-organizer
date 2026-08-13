### Fixed

- Six scheduled tasks reported themselves as enabled while being unable to ever run. Four
  of them — auto-organize, orphaned temp-file cleanup, trash cleanup, and the archive sweep
  — asked to run in the nightly maintenance window but had been left out of the list that
  window actually iterates, so they had never run at all. Between them they clear leftover
  files from interrupted conversions, trashed book versions past their 14-day expiry, and
  deleted books past their 30-day retention. All four are now wired in, and a new test
  fails the build if any future task is added without a way to run.
- The startup warning that reports a task will never run no longer fires for the healthy
  tasks that run nightly rather than on a timer. It had been warning about 13 of 18 tasks,
  which buried the 6 real ones.
