### Changed

- `dedup.breakdown-backfill` was run with `apply=true` against production: 18,311
  pre-T015 pending candidates now carry a `ScoreBreakdown`, `Band` and
  `FormulaVersion` (0 score errors, 0 update errors). No status, layer or
  similarity was touched and no book was merged or deleted. Verified by a
  follow-up dry run: `skipped_has_breakdown` went 838 → 19,149 and the remaining
  759 targets are all zero-signal rows, which are unscorable by design.
