### Fixed

- The RC cleanup workflow no longer aborts partway through a large purge. Each
  deletion is two mutating API calls (the release and its git ref), so clearing a
  backlog of hundreds runs into GitHub's secondary rate limit; a bare
  `gh release delete` under `set -euo pipefail` would abort mid-purge and report a
  red job indistinguishable from a broken workflow. Failures are now tolerated,
  counted, and surfaced as warnings, the loop is paced, and the run summary reports
  a `Failed` count. The operation is idempotent, so a re-run finishes the tail.
