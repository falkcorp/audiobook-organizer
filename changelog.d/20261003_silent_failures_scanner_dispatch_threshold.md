### Fixed

- **A zero chapter threshold no longer turns chapter grouping off.**
  `chapter_consolidation_threshold_min` set to 0 used to disable chapter
  consolidation, and 0 is also what any incompletely filled config carries, so
  a stray zero once stopped multi-file books from being grouped for eleven
  days with no warning. 0 or less now means the default (10 minutes): the
  value is rewritten to 10 with a warning when config is loaded, validated or
  saved, and the scanner and repair jobs treat it as 10 wherever it comes
  from. This reverses the 2026-09-28 note that 0 "only switches off
  consolidation of untagged files": there is no off switch any more.
- **Operations no longer start after shutdown has begun.** A dispatch cycle
  that was already past its shutdown check could hand an operation to a
  worker, which then ran it to completion while the server was stopping. The
  worker now checks for shutdown before it starts a run and leaves the
  operation queued for the next start, and the dispatcher stops claiming
  operations as soon as shutdown begins.
- **Scanner: failed scan-fail-count resets are logged.** Resetting a file's
  failure counter after a successful read discarded both errors and panics,
  so a reset that never landed left earlier failures counting toward
  auto-quarantine with nothing in the log. Both are now logged (sampled) and
  counted in the scan summary. The matching increment after a failed read had
  no panic guard at all, so a panic there (a closed database) crashed the
  server mid-scan; it now gets the same logging and counting. The scanner and
  the quarantine service now compute the counter's key with one shared
  function, so the counter the scanner writes is always the one quarantine
  reads.
