- [ ] Investigate app-dir guard tests failing only under heavy cross-package test load

  Observed 2026-08-30 while finishing `fix/app-dir-guards-remaining-walkers`. Running
  ~24 package binaries concurrently on a saturated dev box produced a DIFFERENT random
  subset of failures on each run:

  - `TestStripMovementAtoms_SkipsAppDirs` (internal/server) — expected 1, actual 3
  - `TestFileProvenanceCapture_SkipsAppDirs` (internal/plugins/maintenance) — expected 1, actual 3
  - `TestBuildFileIndex_SkipsAppDirs` (internal/reconcile) — app-dir files indexed
  - `TestChaptersBackfill_ProgressLabelReportsEligibleCount` — unrelated to these guards

  Controls already run, so do NOT repeat them:
  - `go test -p 1` over all 10 affected packages: **exit 0, zero failures**.
  - `-race` over 4 packages: **zero DATA RACE warnings**.
  - Each package in isolation, and `internal/reconcile` at `-count=5`: all pass.

  The mechanism is UNEXPLAINED. It is not a data race and not a global-config collision:
  `TestBuildFileIndex_SkipsAppDirs` is fully hermetic — it passes a literal
  `pathutil.AppDirs`, walks a single dir (one goroutine), and `pathutil.ShouldSkipDir` is
  pure string logic with no filesystem I/O. A pure function cannot change its answer under
  load, so either the fixture or the walk is not seeing what the test believes it wrote.

  Worth ruling out: `BuildFileIndex`'s walk swallows every error with
  `if err != nil { return nil }` (internal/reconcile/itunes_heal.go:181). Under fd
  exhaustion that yields a silently incomplete index in PRODUCTION, which is a real
  silent-failure defect independent of this test question — though note it would produce
  too FEW indexed files, not too many, so it does not by itself explain what was seen.
