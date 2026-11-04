### Fixed

- CI: every GitHub job that restores the shared Go cache now frees runner disk
  first, through one script (`scripts/ci/free_runner_disk.sh`). On 2026-10-31
  only the race test shards got this. On 2026-11-04 Coverage Floor (#4361) and
  Errcheck Ratchet (#4362) ran out of disk partway through restoring the
  ~2.6 GB cache. They failed with no log, because the runner could not write
  even its own diagnostics.
