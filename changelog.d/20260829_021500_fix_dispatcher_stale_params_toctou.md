### Fixed

#### A request merged into a queued operation could be silently dropped

`dispatchCycle` snapshots every queued row via `ListQueuedOperationsV2()`
**without** holding `r.mu`, then claims a row later under the lock. For any def
declaring `MergeQueuedParams`, a merge could land in that gap:

1. `dispatchCycle` reads row X, `Params={book_ids:[A]}`.
2. `EnqueueOp` merges book B, persists `{book_ids:[A,B]}`, and hands the caller
   X's operation id — so the caller believes B is queued.
3. `dispatchCycle` reaches X and claims it. X was genuinely unclaimed at step 2,
   so `tryMergeQueuedParams` was right to merge into it.
4. The `queuedRun` was built from the **step 1 snapshot**, so the run applied
   `[A]` only.

Book B was never processed, progress reported `complete` against a total that
never counted it, and the operation row showed `[A,B]` forever. A silent drop
with a success receipt — and the caller had no way to detect it.

This affected all three defs that gained `MergeQueuedParams`:
`metadata.batch-apply-cached`, `metadata.batch-save`, and
`library.bulk-write-back`. It is the same shape as the 2026-08-21 incident that
introduced the merge feature — that fix closed the large hole and left a smaller
one in the same place.

The merge side was already correct. `tryMergeQueuedParams` holds `r.mu` across
its whole read-merge-write and skips any row already in `r.running`, so the
claim is a real barrier: once published, no further merge can touch that row.
That is what makes a plain re-read sufficient. The dispatcher now re-reads the
row after claiming it, and both interleavings are safe — merge-then-claim is
caught by the re-read, claim-then-merge is refused by the merge.

Failure is handled closed. If the re-read errors, or the row vanished (cancel or
delete between snapshot and claim), the claim is released and the operation is
retried on a later cycle rather than running with params that could not be
confirmed — running the snapshot on a read error would be the exact drop this
guard exists to prevent. Releasing the claim also has to remove the stub handle
from `r.running`, since Gate 0 consults it and a leaked handle would strand the
operation permanently; that undo is now a single `releaseClaim` helper shared
with the pre-existing worker-channel-full path instead of being duplicated.

Only defs that actually declare `MergeQueuedParams` can change while queued, so
every other operation keeps the snapshot and pays no extra read.

All four properties are mutation-tested — shipping the snapshot, failing open on
a read error, skipping the claim release, and dropping the merge-only narrowing
each fail a specific test. Package passes under `-race`.
