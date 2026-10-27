### Changed

- The Woodpecker `test-fixtures` workflow is now three parallel workflows, `test-fixtures-1`, `-2` and `-3` (status contexts `ci/woodpecker/pr/test-fixtures-N`), one shard each with the full 25m per-package timeout. Run back to back on llm1 the shards summed to about 3400-3500 s and the last one (`internal/server`) timed out against the shared 55 min budget. `scripts/ci/run_fixture_shards.sh` is deleted.
