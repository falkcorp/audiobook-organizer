### Fixed

- CI: `check-commits-build` now runs as 4 parallel shards (`check_commits_build.py --shard K/N`), each building one contiguous slice of a move PR's commits. A 48-commit PR (#4453) ran past the job's 30-minute cap, which 09 §9.2.10 fixes for every extraction-gates job, so the work is split instead of the timeout raised. Shard 1 also runs the rename-similarity check.
