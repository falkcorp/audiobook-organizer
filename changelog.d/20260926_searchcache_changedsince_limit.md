### Added

- Search result cache: new counter `audiobook_organizer_search_cache_rebuilds_total` exports `Stats().Rebuilds` (every build the cache starts, including first builds after a miss), next to the patch-cap counter, so rebuild frequency can be compared when tuning the change ring.

### Changed

- Search result cache: `ChangeLog.ChangedSince` takes a limit and stops once it has `MaxPatchChanged+1` distinct changed books, and it holds the change-log mutex only for a binary search plus a copy of the window. The dedupe now runs after the mutex is released. A stale whole-ring lookup at 65,536 records drops from 7.45 ms and 12.5 MB of garbage to 165 µs and 210 KB. A book write's `Record` p99.9 wait during such lookups drops from 13.1 ms to 62 µs.

### Fixed

- `GET /api/v1/activity?exclude_tags=def:<op id>` now hides a renamed operation's whole history by either spelling (`ActivityFilter.ExcludeTagAliases`) in the Nuts, SQLite and Pebble activity stores, matching `?tags=`.
