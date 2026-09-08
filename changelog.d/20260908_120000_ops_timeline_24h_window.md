### Fixed

- **The operations list looked empty after every restart.** It showed only the
  last 15 minutes of history, so on a freshly restarted server — where nothing
  has finished recently — a fully populated history rendered as "no operations".
  Nothing was ever deleted; no code deletes operation records at all. The view
  now shows the last 24 hours.

- **Long operations were missing from their own history.** An operation was
  matched to the time window by when it was QUEUED, not when it finished, so a
  backfill queued 30 hours ago that completed 20 minutes ago did not appear in a
  24-hour view. The longer an operation ran, the more likely it was to be
  excluded — the opposite of useful. Finished operations are now matched on when
  they completed.

- **A capped operations list no longer looks like missing data.** The client
  asked for no limit, taking the server's default of 200, and ignored the
  server's own `truncated` flag. It now requests the maximum and logs a warning
  naming how many were left out.
