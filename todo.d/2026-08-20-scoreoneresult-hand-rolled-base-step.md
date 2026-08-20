- [ ] **SCORE-REC** Route `ScoreOneResultWithBreakdown` through `scoreRecorder`
      like `ApplyNonBaseAdjustmentsWithBreakdown` now is. It still hand-builds
      its own `ScoreOpBase` step at `internal/metafetch/service_scoring.go:724`,
      duplicating what `newScoreRecorder` does. This is the last hand-rolled
      `ScoreStep` site left after #2639, which converted the sibling function
      because `scoreRecorder.add` had been flagged unused — the linter can only
      see the unused helper, never the copied logic that should be calling it,
      so nothing will flag this one. Done means: no `ScoreStep` composite
      literals outside `score_breakdown.go`, and the golden fixtures in
      `service_scoring_test.go` still pin the same totals (verify by mutation,
      not by a green run — halving a factor must fail them).
