### Changed

- The db-health deep expired-row count now reports how long its walk took and
  how many 1000-row pages it read (`expired_entries_elapsed_ms`,
  `expired_entries_pages_walked`, also logged), and a walk that hits the
  2-minute ceiling says "timed out after 2m, N pages walked" instead of a bare
  "context deadline exceeded". A panic inside the shared walk is now logged
  with its stack trace.

### Fixed

- Diagnostics: refreshing the Database Health card clears a stale
  "Expired count failed" caption, and the TTL field reads "TTL off" instead of
  "0 days" or "-1 days" when the cache TTL is disabled.
