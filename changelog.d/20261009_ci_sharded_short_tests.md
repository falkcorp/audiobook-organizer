### Changed

- CI runs the short Go test suite once, in four parallel shards (`Go Tests (short, race) shard k/4`), instead of twice in full: `scripts/ci/short_test_shards.py` splits the heavy packages by test name, checks that every test is assigned to exactly one shard before running, and the `Coverage Floor (PR gate)` job merges the four coverage profiles and applies the unchanged floor. Minimal CI's own `Go Tests (short, race)` job is switched off through the new `run-go-tests` input. `make test-short-shard SHARD=k/N` runs one shard locally.
