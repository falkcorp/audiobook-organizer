- [ ] **FLAKE-CANDIDATE-WRITE-PATH** `TestCandidateWritePath_ConcurrentNoRace`
      (`internal/database/embedding_store_candidate_durability_test.go`) failed
      once in 0.11 s during a `-race -count=20` run of the package's
      Racing/Concurrent/Race/Parallel/Fsync/Warmup/Chaos tests on a loaded Mac
      (2026-10-03, branch `test/database-package-speed`). The failure message
      was not captured. Tally so far: 1 fail in ~48 broad `-race` iterations,
      0 in ~1,550 isolated ones (`-count=300` at `-cpu` 1/2/4/16, plus 350
      more). The test moved from an on-disk to an in-memory Pebble store in that
      branch, so tighter write interleaving may be what exposed it; treat it as
      introduced there until shown otherwise. Next step: rerun the broad
      command with full output kept (`-v` piped to a file) until it fails,
      read which assertion fired, and fix the cause in its own PR.
