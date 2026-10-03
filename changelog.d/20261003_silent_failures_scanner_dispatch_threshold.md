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
  operations as soon as shutdown begins. Operations that had been handed out
  but not yet started when shutdown began are put back as queued. Before,
  they could be marked interrupted, and an operation set to be dropped on
  interruption was lost without ever having run.
- **Scanner and quarantine: scan-fail counter failures are logged.** Each
  file has a failure counter, and auto-quarantine acts on it. Resetting the
  counter after a successful read discarded both errors and panics, so a
  reset that never landed left earlier failures counting toward
  auto-quarantine with nothing in the log. The increment after a failed read
  had no panic guard. Both now log (sampled) and are counted in the scan
  summary. This does not keep a scan going on a database that has been
  closed: the scan's next unguarded database call still stops it, which is
  correct. What changes is that the counter failure is reported instead of
  hidden. The quarantine side no longer reads an unreadable counter as zero:
  the database now reports the read error, and the auto-quarantine pass logs
  and counts failed reads, a failed book listing and failed quarantine moves.
  The scanner and quarantine compute the counter's key with one shared
  function, so the counter the scanner writes is always the one quarantine
  reads.
