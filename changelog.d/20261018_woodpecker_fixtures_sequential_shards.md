The Woodpecker `test-fixtures` workflow now runs its fixture packages as three
sequential shards (`FIXTURE_SHARD=1/3`, `2/3`, `3/3`) in one step instead of
every package at once. On the slower heavy agents the packages were contended
(temp-file I/O and CPU) and `internal/plugins/maintenance` timed out at the 25m
per-package limit with nothing hung; sharding keeps the heavy packages
(database, maintenance, scanner/server) from all overlapping. The status
context, the agent, the 25m default timeout and the set of packages tested are
unchanged. `scripts/ci/run_fixture_shards.sh` runs every shard even if one
fails, prints per-shard elapsed seconds, exits with the combined status, and
gives each shard `min(25m, budget - elapsed)` of a 55 min step budget so Go
times out and dumps before Woodpecker's 60 min repo timeout kills the job.
`make test-fixtures` takes a new `FIXTURE_TIMEOUT` (default `25m`).
