### Changed

- `maintenance.purge-empty-authors` now has a 2-hour run timeout instead of 30 minutes. Each delete sweeps the whole book-author junction, about one delete per second on production, so a 3,958-author apply timed out after 1,742 deletes on 2026-09-12. Two code comments that described that sweep as a cheap seek over an empty keyspace, and the scan stand-down as missing a resumed scan, are corrected.
