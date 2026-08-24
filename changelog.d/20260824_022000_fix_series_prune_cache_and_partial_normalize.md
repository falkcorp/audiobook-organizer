### Fixed

#### A refused series merge no longer leaves the series list stale

Making the merge fail-closed introduced this, and it was caught in review before it
reached anyone's library.

The cached series list is dropped only when a run "cleaned" something, on the
stated reasoning that *"a run that cleaned nothing must NOT invalidate: it changed
nothing."* That held when a merge either completed or errored out.

Refusing the delete created a third outcome: books get repointed to the surviving
series, and then nothing is removed. The cleaned count stays at zero, so the cache
is kept — while every one of those books now belongs to a different series. The
series list goes on serving pre-merge membership for up to 24 hours.

That is the same stale-list symptom measured in production on 2026-08-14, quoted in
the very comment that justified the condition, reached from the opposite direction.
Repointed books are now counted in their own right, and the invalidation message
reports both numbers.

#### A partial series-normalize no longer discards the work that succeeded

Also introduced alongside the fail-closed change. Recording a collection error
turned a swallowed failure into one that aborted the operation — which skipped
organize and tag write-back for **every** book in the run, not just the series that
failed.

The renames and merges have already committed by the time that error surfaces, so a
re-run finds nothing to normalize, computes no actions, and those files are never
organized: the failure was permanent rather than retryable. The operation now
organizes and retags the books it did collect, and then reports the failure. The
status is still `failed` — deferring it buys file consistency, not silence.

This is a deliberate trade-off between two imperfect outcomes. Organizing moves
files to match series rows that genuinely changed; not organizing leaves committed
renames with their files under the old paths and no way to retry. The second is
worse, and silent.

### Changed

#### A residual was renamed because its old name understated it

`SERIES-MERGE-TRASHED-ROWS-RESIDUAL` is now `SERIES-MERGE-UNGUARDED-DENOMINATOR`.

Every guard added by the series-merge work counts against *what the membership
getter returned*, and that getter reads the in-memory index unconditionally when
warm, with no completeness check of its own. Two populations sit outside the guard,
not one:

- **Books in the trash**, excluded by design. Latent — it bites when one is restored.
- **Books the in-memory index has lost** while their on-disk row survives. There are
  four documented causes, one of which needs no restart. That is a **live, primary,
  untrashed** book: the getter never returns it, the guard's failure count stays at
  zero, the delete proceeds, and the row is stranded immediately with nothing
  raised.

The second is structurally the same defect this work removed from the
series-renumbering job — a guard whose sample space is the filtered getter's own
output, so the rows the bug lives on can never trip it. It was reintroduced one
layer up while fixing the original.

The two halves share one fix but are not one decision: refusing on a trashed row
changes what a healthy run does, while refusing on a lost index row only fires when
the store is already known-degraded and prevents immediate loss. The second is worth
splitting out if the first stalls.
