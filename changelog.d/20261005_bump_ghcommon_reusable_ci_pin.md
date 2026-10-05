### Changed

#### CI: reusable-ci pin moved to github-common 8eb9a5a

- `frontend-ci.yml`, `nightly.yml` and `.github/ghcommon-ref.txt` now point at github-common `8eb9a5a` (github-common#365). The Go CI job no longer re-runs the whole test suite for a coverage checker that exits at once without `.testcoverage.yml` (this repo has none), and its cap is 40 minutes instead of 30, so a hung test is killed by Go's 10-minute test timeout with a goroutine dump rather than a bare runner cancel. The pull brings the reusable workflow's action pins forward (checkout/setup-* v7); every job in it still asks for `contents: read` only, so this repo's `permissions:` blocks need no change.
