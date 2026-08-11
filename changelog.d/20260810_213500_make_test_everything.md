### Changed

#### `make test-everything` replaces `scripts/run-all-tests.sh`

The 123-line script is deleted. Its one capability that `make` could not already
do — running backend, frontend and e2e in a single pass, *continuing past a
failing surface*, and ending in a pass/fail matrix — is now a Makefile target.
`make test-all` is `test web-test` (no e2e), and make is fail-fast, so
`make test-all test-e2e` stopped at the first failing surface instead of
reporting all three.

Two latent defects in the script are fixed by construction rather than carried
over:

- It never cleared a stale server on `:8484`. Playwright's `reuseExistingServer`
  is on outside CI, so it would silently attach to whatever was already
  listening and the e2e verdict could describe a **different build** than the one
  just compiled. The target now kills a stale listener first and says so.
- It backgrounded `npx playwright show-report --port 9323 &` and never killed it,
  leaving a server running after exit. The target does not serve a report.

This resolves TOOL-8 from `docs/audits/2026-06-22-repo-optimization-security-sweep.md`,
open since June and left undecided by the earlier correctness fix.
