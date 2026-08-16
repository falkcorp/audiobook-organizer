### Added

- Every background operation must now declare how it reports its own progress,
  and the system refuses to accept one that doesn't. Reporting progress had
  always been the rule, but nothing enforced it, so an operation that was never
  wired up to report looked identical to one that had genuinely frozen — both
  went quiet, and both got cancelled after five minutes. That ambiguity is what
  let a broken progress connection survive three months and get worked around
  three times as if it were a slow operation. There are three permitted answers:
  the operation reports automatically by processing a list of items, it reports
  by hand, or it declares that it does not report at all — and the last one has
  to say how long it may run in silence, and is listed in the startup log so the
  set of unmonitored operations stays visible instead of growing unnoticed.

### Changed

- Writing a log line no longer counts as reporting progress, and this is
  deliberate: an operation stuck in a loop that logs is still stuck, and
  treating chatter as a sign of life would blind the watchdog to the exact
  problem it exists to catch.

### Fixed

- The nightly maintenance window is no longer cancelled part-way through for
  being "stuck" while it is simply waiting. It runs each maintenance task in
  turn and waits for it to finish, and a single task routinely takes longer than
  the five minutes of silence the watchdog allows — so a perfectly healthy run
  looked identical to a frozen one. It was 28 of the 44 operations cancelled for
  inactivity in the preceding month. It now reports the running task's own
  progress while it waits, so a real freeze is still caught: the task it is
  waiting on is watched independently.

- Library scans no longer die at the AI-parsing stage. After walking every file
  the scan hands filenames to the AI for cleanup, in batches — and that stage
  never reported progress, so a scan that had already done all its real work got
  cancelled for inactivity five minutes in and threw the whole walk away. It now
  reports per batch. Separately, when the AI provider returns something that
  cannot resolve on its own — no credits, a revoked key — the scan stops asking
  instead of working through every remaining batch against the same answer, and
  says so in the log. Those books keep the metadata derived from their
  filenames. A scan of a 3,917-file library was failing this way on 2026-08-16
  because the OpenAI account had run out of credits.
