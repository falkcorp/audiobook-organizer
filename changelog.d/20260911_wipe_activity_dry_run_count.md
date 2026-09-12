### Fixed

- The activity wipe's dry-run preview now reports the exact number of rows the wipe would delete, using the new `database.CountAllActivity`, which is an exact per-tier count on the active backend. It previously read `Query`'s `total` with `Limit: 1`, a pagination probe that stops at two matches, so the preview said "2" regardless of how many rows existed.
