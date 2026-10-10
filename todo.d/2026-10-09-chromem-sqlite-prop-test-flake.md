- [ ] **PROP-CHROMEM-FLAKE** `TestProp_ChromemMatchesSqlite`
      (`internal/server/dedup_engine_prop_test.go:387`) failed once on `main`
      run 37995734374 attempt 1 ("sqlite→chromem overlap too low: 0 of 1
      matched (chromem set=0)") and passed on the auto re-run. The 07-C4
      throughput record (`docs/ci/2026-10-ci-throughput.md`) lists it as the
      only re-run in the first ten merges after 07-C3. It is a random-seed
      property test, so the counterexample is lost unless the seed is logged:
      make the test print its seed on failure and pin that seed in a regression
      case, then find out why chromem returned an empty set for an input sqlite
      matched. A test that passes on re-run is a bug with a hidden input, not
      noise.
