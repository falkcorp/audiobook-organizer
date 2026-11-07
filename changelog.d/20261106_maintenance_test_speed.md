### Fixed

- The `internal/plugins/maintenance` test package no longer runs into the CI test timeout. It spent its time waiting on fsync from 47 test files that each opened an on-disk store, not computing; they now open in-memory stores, and the two 1,001-book page-streak tests share one source directory. Package `-short -race` wall time on a loaded Mac went from 11m42s to 3m06s, non-race from over 10 minutes (it timed out at the default budget) to 1m50s. A guard test keeps new tests from opening on-disk stores without a reason.
