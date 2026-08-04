<!-- file: todo.d/2026-08-03-flaky-apply-pid-repair-same-file.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6f10b7e4-9c25-4d83-a0f6-14b7e29d3c05 -->
<!-- last-edited: 2026-08-03 -->

- [ ] **Flaky: `TestApplyPIDRepairSameFile`** (`internal/itunes`) failed
      `Minimal CI / Go Tests (short, race)` on PR #2126 — a PR that touches only
      `internal/server/server_maintenance_deps.go` and cannot affect the iTunes
      package.
      Verified as a flake, not a regression: **10 consecutive passes on the PR
      branch and 10 on `main`**, both with `-race` exactly as CI runs it.
      This is the **second** flake found on 2026-08-03; see
      [[2026-08-03-flaky-backfill-syncids-race-sanity]]. Two independent flaky
      tests blocking unrelated PRs in one evening suggests a shared cause worth
      one investigation rather than two: both are concurrency tests, both pass
      locally, both fail only under CI load. Suspect a shared fixture, a fixed
      sleep, or an unsynchronised goroutine handoff that only loses the race on
      a slower/contended runner.
      Do NOT keep re-running them — that is how a flake becomes permanent and
      how a real regression eventually gets waved through. Related:
      [[project_ci_gotests_intermittent_stalls]].
