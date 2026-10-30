### Fixed

#### Fixture-test shards no longer die on the 25m `go test` timeout

`test-fixtures-N` (`make test-fixtures`) panicked with `test timed out after
25m0s` on `internal/server` (pipeline 1823, `TestThrottle_TriggersAfterMaxIPFailures`
running) and on `internal/plugins/maintenance` (pipeline 1820, `TestFragmentFixer_*`).
Measured in Woodpecker those two packages took about 1300 s each, against a 1500 s
timeout, and the shard weights in `scripts/ci/fixture_test_packages.py` were stale
(`internal/server` listed at 160 s).

- `scripts/ci/fixture_test_packages.py` now weights packages by their measured
  Woodpecker wall time, and a package whose weight exceeds 600 s is split by test
  name into several `go test -run '^(...)$'` processes (`internal/plugins/maintenance`
  3 ways, `internal/server`, `internal/audiobooks` and `internal/scanner` 2 ways)
  that the greedy balancer spreads across shards. The split is by a hash of the
  top-level test name, so it does not depend on the host. A slice that selects no
  test fails the run instead of reporting ok. Estimated shard loads go from
  uneven (shard 2 and 3 near the limit, shard 1 light) to 2241 / 2240 / 2238 s.
- `make test-fixtures` runs the planned invocations side by side and fails on any
  non-zero exit or `[no tests to run]`; `FIXTURE_TIMEOUT` now applies to each process.
- The `internal/server` tests were slower than they needed to be. Failed logins for
  an unknown name ran a full-cost argon2id verification (64 MiB, about 1.6 s under
  `-race`), and the login throttle slept for real (up to 2 s per failure past the
  5th). The test binary now hashes at a minimal cost
  (`absauth.SetHashCostForTest`, called only from `internal/server/main_test.go`;
  a guard test fails if non-test code references it), and the throttle tests inject
  a no-op sleep. `TestThrottle_TriggersAfterMaxIPFailures` goes from 40 s to under
  0.1 s; tests slower than 10 s in `internal/server` go from 21 (422 s) to 3 (50 s).
  Every assertion is unchanged.
