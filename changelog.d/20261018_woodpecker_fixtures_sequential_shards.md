The Woodpecker `test-fixtures` workflow now runs its fixture packages as three
sequential shards (`FIXTURE_SHARD=1/3`, `2/3`, `3/3`) in one step instead of
every package at once. On the slower heavy agents the packages starved each
other of CPU and `internal/plugins/maintenance` timed out at the 25m
per-package limit with nothing hung. The status context, the agent, the 25m
timeout and the set of packages tested are unchanged.
