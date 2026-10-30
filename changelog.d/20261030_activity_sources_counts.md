### Fixed

- `GET /activity/sources` counted entries the caller may not read (hidden Repairs runs and other users' data lines), so the source picker disagreed with the activity list. The store's source count now honours the same per-caller exclusion, and a source whose only entries are hidden no longer appears. Counts for a restricted caller are computed per request rather than served from the shared result cache.
