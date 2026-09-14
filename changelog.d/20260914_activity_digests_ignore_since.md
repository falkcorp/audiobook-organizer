### Fixed

- Activity page: daily digests are now listed in full instead of only today's.
  Digests are one row per UTC day stamped at 00:00, so the default 24-hour
  "Since" window hid every one but the current day's. The paged feed now always
  excludes the `digest` tier, and a separate `tier=digest` request (no `since`,
  `until`/search/tags honoured, limit 400) renders every digest below the feed,
  newest first, under a "Daily digests (N)" subheader. Digests no longer count
  toward the feed total, and background polling refreshes them silently.
