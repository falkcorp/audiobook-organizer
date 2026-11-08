### Fixed

- CI: `check-commits-build` now runs as 4 parallel shards (`check_commits_build.py --shard K/N`), each building one contiguous slice of a move PR's commits. A 48-commit PR (#4453) ran past the job's 30-minute cap, which 09 §9.2.10 fixes for every extraction-gates job, so the work is split instead of the timeout raised. Shard 1 also runs the rename-similarity check.
- CI: `dashboard-reference` gets promtool from the first source that works: the cached, checksum-verified release tarball; the release download, retried for 90 seconds; or a build of the same release from the Go module proxy (about 35 seconds). GitHub's release CDN returned 504 to runners for over an hour, and 5 minutes of retries did not get through, which failed this required check on every PR.
