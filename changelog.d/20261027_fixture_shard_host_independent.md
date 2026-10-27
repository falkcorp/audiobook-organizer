### Fixed

- `scripts/ci/fixture_test_packages.py` now computes the fixture-test shard
  partition under a fixed `GOOS=linux GOARCH=amd64 CGO_ENABLED=1`, so the three
  `test-fixtures-N` Woodpecker workflows agree on the package list whichever
  agent (linux/amd64 or darwin/arm64) runs them. Before, OS-specific test files
  and build tags could shift the greedy assignment so a package ran in no shard
  or in two. A package assigned to the running shard that this host cannot test
  now fails the job with a clear message instead of being skipped, and a short
  hash of the partition input is printed so shard logs can be compared.
