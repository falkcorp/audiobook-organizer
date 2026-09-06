### Fixed

- **The dashboard's "Broken Files" tile now reports the real number instead of
  always showing 0.** It was reading a secondary index (`book_file_errors_by_book:`)
  that has no live writer, so it counted zero broken books on a library that in
  fact has thousands. The counter now derives from each file row's `Missing`
  flag — the count of distinct primary books that own at least one file whose
  bytes are gone — computed inline on both the in-memory fast path and the Pebble
  fallback, which are covered by the same conformance test so they cannot drift.

### Added

- **New maintenance operation `maintenance.mark-missing-files`** reconciles every
  book file's stored `Missing` flag with what is actually on disk: it sets the
  flag where the bytes are gone and clears it where they have returned (e.g. after
  a repoint restored the path). This is what keeps the Broken Files counter honest
  without stat-ing the whole library on every dashboard refresh. It writes only
  the flag — never moves or deletes anything — defaults to a dry run that reports
  exactly what it would change, and re-stats each row immediately before writing
  so a row whose state changed underneath it is skipped rather than written stale.
