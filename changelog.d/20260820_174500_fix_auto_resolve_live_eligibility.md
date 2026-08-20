### Fixed

#### Auto-resolve's suppressor guard could never fire

`autoResolveEligible` refused a CERTAIN pair whose `ScoreBreakdown.Suppressors`
list was non-empty — but no production code path has ever populated that field.
`unified.ComposeScore` is its only writer, and all three live callers
(`engine.go`, `rescore.go`, `calibrate_composite.go`) pass `nil`. The scan path
compounds it: a pair that fails `PairEligibility` is DELETED outright rather
than scored, so a suppressed pair never acquires a stored breakdown at all. An
empty list was therefore the only value the code could persist, and the guard
passed vacuously on every candidate in the library — not just the 18,311 rows
written by `dedup.breakdown-backfill`.

`autoResolveEligible` now evaluates `PairEligibility(bookA, bookB)` LIVE at the
gate. Both book records are already in hand there and the predicate is pure with
no I/O, so this costs nothing per pair. It also catches a pair that became
suppressed after its score was written — a version group assigned later, say —
which a stored snapshot could not.

The stored-list check is retained ahead of it rather than replaced, so a legacy
row written by an older binary that did record suppressors still refuses, with a
distinct reason string in the audit sample saying which guard fired.

The pre-existing regression test set `Suppressors` directly on the fixture, so
it passed against the broken code. The two new cases leave the stored list empty
and make the books themselves suppressed — the shape real data actually takes —
and both fail when the live check is removed.
