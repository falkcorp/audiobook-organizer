- [ ] **FLAKE-WRITEBACK-RESUME** `TestBulkWriteBack_ResumeSkipsCheckpointedBooks`
      (`internal/server/library_writeback_resume_test.go:76`) is flaky: it
      cancels the context when the nth callback reaches `n/2`, but the bulk
      write-back runs concurrent workers that can finish all 60 books before
      the cancel is observed, so the resume assertion fails with
      "checkpoint owes 0 of 60". Failed CI on PR #3449 at 2026-09-19T06:20Z;
      passed 8/8 locally. Fix: make the cancel point deterministic (block the
      remaining workers until the cancel lands, or run the first pass with
      concurrency 1) instead of racing the pool.
