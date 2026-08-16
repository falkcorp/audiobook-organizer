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
