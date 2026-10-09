### Changed

- The short-test shard planner estimates every test at no less than 0.25 s (a thousand tests recorded as 0.00 s are not free: measured 0.27 s each on the runner) and uses package weights measured on the first warm-cache run, so the heavy packages split into groups of even size and the longest shard no longer carries one group of a thousand "free" tests.
- The short-test shards' race build cache refreshes from `main` every six hours instead of once a day: with ten-plus merges a day, a day-old entry turned mostly cold within three hours (a shard went from 4.5 to 8.5 minutes against the same entry).
