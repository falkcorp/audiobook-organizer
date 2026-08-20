### Fixed

#### `make ci`'s staticcheck gate is green again

Three findings had accumulated on `main` and reproduced on a clean checkout, so
every `make ci` run was red before any new work started. The gate is not a
required check today, which is how they survived.

Two were genuinely inert and are simply removed: a redundant `score =
asinRec.score` in the ASIN branch of `service_search.go` that was overwritten
unconditionally eleven lines later, and a vestigial `errs` field on the
`captureRegistry` test double, whose `RegisterOp` hardcodes `return nil` and
never appended to it.

The third was not inert. `scoreRecorder.add` was reported unused, but it was
unused because `ApplyNonBaseAdjustmentsWithBreakdown` hand-built its own
`ScoreStep` literals instead of going through the recorder — an additive step
that duplicated `add` field for field, and a multiplicative one that duplicated
`mul`. Deleting `add` would have silenced the linter and left the duplication
permanently unflagged, since nothing else would ever point at it. The function
now uses `newScoreRecorder`/`mul`/`add`, which restores the invariant the
recorder exists for: a factor cannot be applied without being recorded, because
applying it *is* recording it.

That conversion is covered by the golden fixtures that pin these totals
bit-for-bit — verified by mutation rather than assumed, since a green suite
proves nothing about a path it does not exercise: halving the rich-metadata
bonus fails 11 assertions, halving the compilation penalty fails 7.
