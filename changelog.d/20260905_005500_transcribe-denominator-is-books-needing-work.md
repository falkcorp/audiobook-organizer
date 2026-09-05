### Fixed

- **`maintenance.intro-transcribe` progress denominator is the work, not the library.**
  With `only_missing` (the default) the numerator counted books attempted while the
  denominator counted every book in the library, so a run over 2,000 untranscribed
  books in an 83,000-book library read "1,200 / 83,228" when it was 60% done, and
  `stats:transcribe.total_books` said the same. The run now decides its work up front
  (a bounded parallel read of every listed book, order preserved for checkpoint/resume),
  reports `library_books` / `total_books` / `skipped_existing` / `unreadable` on the
  start line, pages over the work list only, and `total_books` in the aggregate is the
  number of books the run set out to attempt. Books the store lists but cannot return
  are counted and warned about rather than silently dropped. A run with nothing to do
  still publishes its skip count and marks itself done.
  The aggregate gains `deferred` (books inside `total_books` that got no outcome —
  an endpoint outage, a failed temp dir, a row unreadable at page time; retried next
  run) and `unreadable` (listed books the selection could not read), so
  `attempted + deferred == total_books` at the end of a complete run. A store that
  fails every read is a run error, never a successful "nothing to transcribe".
