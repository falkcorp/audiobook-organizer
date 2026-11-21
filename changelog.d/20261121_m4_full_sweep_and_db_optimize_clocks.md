### Fixed

- The task cutover (M4) no longer moves the weekly full library sweep: `library_scan_full`'s interval clock was HEAD's hourly due check, stamped every hour whether or not a sweep ran, and M4 merged it into the sweep's clock, so the first sweep after cutover would have waited a full week from the cutover hour instead of a week from the last sweep. The sweep's clock is now `library_scan_full_last_run` alone and the tick row is dropped.
- M4 now gives every db-optimize clock (the `db_optimize` interval clock and the v3 schedule state of `maintenance.db-optimize` or its former ID) to both halves of the split kind, `core.storage.compact` and `books.maintenance.aux-store-compact`. Before, the books half imported no clock and first fired one full period after cutover.
