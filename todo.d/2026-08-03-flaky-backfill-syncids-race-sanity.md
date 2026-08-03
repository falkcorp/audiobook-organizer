<!-- file: todo.d/2026-08-03-flaky-backfill-syncids-race-sanity.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2e58c9a1-7b34-4f60-a812-3d90f6c47b25 -->
<!-- last-edited: 2026-08-03 -->

- [ ] **Flaky: `TestBackfillSyncIDsJob_ConcurrentRaceSanity`** (`internal/maintenance/jobs`)
      failed the Coverage Floor gate on PR #2123, a PR that touches only
      `internal/server/middleware/absauth.go` and cannot affect this package.
      Verified as a flake, not a regression: **10 consecutive passes on the PR
      branch and 10 on `main`** locally. It fails only under CI load, which fits
      a timing-sensitive concurrency assertion.
      Do not just keep re-running it — find the timing assumption (likely a
      fixed sleep or an unsynchronised goroutine handoff) and make the test wait
      on a condition instead of a duration. Related: [[project_ci_gotests_intermittent_stalls]].
