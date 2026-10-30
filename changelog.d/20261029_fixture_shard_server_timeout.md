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
  3 ways, `internal/server`, `internal/audiobooks` and `internal/scanner` 2 ways).
  The split is by a hash of the top-level test name, so it does not depend on the
  host. The balancer puts the parts of one package in different shards (a unit
  prefers a shard that holds no sibling part), so each shard's estimated load comes
  out at 2240 s where shards 2 and 3 were near the limit and shard 1 was light.
- `make test-fixtures` runs the planned processes under a job budget
  (`FIXTURE_JOBS`, default the CPU count): the slices run in a pool of
  `min(slices, budget / 2)`, heaviest first, and the whole-package run gets
  `-p budget - pool`, so the test binaries alive at once stay at or under the
  budget the single `go test` used to have (parallel `-race` suites have OOM-killed
  CI steps). `FIXTURE_TIMEOUT` applies to each process. A slice that selects no test,
  or whose `-run` matches none, fails instead of reporting `ok [no tests to run]`.
  Each slice logs its test count and a hash of its test names; each child's output
  goes to a log file as it is produced (`FIXTURE_LOG_DIR`) and is printed when it
  finishes, or on SIGTERM.
- `go test -list` parsing (shared with the short-suite sharder in
  `scripts/ci/go_test_shards.py`) now accepts any Unicode test name, where a name
  outside `[A-Za-z0-9_]` was silently dropped from every shard, and fails on a
  single token that looks like a test name but is not a valid one. On a host other
  than linux, `make test-fixtures` also compares the native test list with a source
  scan of the linux test files and prints a `::warning::` naming any test that will
  not run there; it does not fail, because a darwin agent legitimately lacks
  linux-only tests.
- The `internal/server` tests were slower than they needed to be. Failed logins for
  an unknown name ran a full-cost argon2id verification (64 MiB, about 1.6 s under
  `-race`), and the login throttle slept for real (up to 2 s per failure past the
  5th). The test binary now hashes at a minimal cost (`absauth.SetHashCostForTest`,
  called from `internal/server/main_test.go`), and the throttle tests inject a no-op
  sleep. `TestThrottle_TriggersAfterMaxIPFailures` goes from 40 s to under 0.1 s;
  tests slower than 10 s in `internal/server` go from 21 (422 s) to 3 (50 s). Every
  assertion is unchanged. `SetHashCostForTest` panics outside a test binary and
  after the dummy hash is built, and `internal/arch`'s `TestHashCostSeamStaysInTests`
  fails if any non-test file in the module, the root `main` package and
  build-tagged files included, names it.

Follow-up, not done here: the balancer still weights by the CI wall-time medians
recorded in `_WEIGHTS`; weighting by per-test recorded durations (as
`go_test_timings.json` does for the short suite) would balance the slices by time
rather than by test-name hash.
