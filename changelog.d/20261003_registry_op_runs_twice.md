### Fixed

- **A background operation could run twice under the same id.** The dispatcher
  decides what to start from a list of queued operations it reads once per
  cycle. If an operation was started and finished while that list was still
  being worked through, the list still said "queued" and the operation was
  started again. The window is as long as the cycle, so it grew with the queue,
  and it hit fast operations hardest. The dispatcher now re-reads every
  operation after claiming it, and a worker only starts an operation if it can
  move it from queued to running in one conditional step. That also stops three
  narrower cases: an operation re-run because its "running" status failed to
  save, an operation run after a cancel that landed at the same moment, and a
  library scan started in the instant a maintenance hold ended.
- **CI: `TestDispatcher_PriorityOrderingHighBeforeLow` failed intermittently.**
  It was reporting the bug above (one operation, two runs), not a test problem.
