### Changed

- `internal/maintenance/jobs` — `deleteOldOperations` is split three ways as a worked example
  of removing a store dependency rather than narrowing it (audit §7, Option C):

  - `operationsOlderThan(ops []database.Operation, cutoff time.Time) []string` — **pure**. No
    store, no context, no `dryRun`. It is the entire retention decision.
  - `expiredOperationIDs(ctx, operationLister, cutoff)` — the read step. Keeps the
    single-`ListOperations`-call guarantee and the assertions that pin it.
  - `deleteOperations(ctx, operationDeleter, ids)` — the write step.

  The `dryRun` flag no longer travels down the call stack; `Run` branches on it and either
  reports `len(expired)` or calls `deleteOperations`. Neither the selection nor the deletion
  has a mode it can get wrong.

  `retentionOperationStore` (2 methods) is replaced by `operationLister` and
  `operationDeleter`, one method each — once the decision is out, both I/O halves are
  single-method.

  Test effect, which is the point of the exercise: `TestRetentionBoundaryLogic` used to assert
  `opTime.Before(cutoff) == want`, comparing `time.Before` against itself and never calling
  production code — it could not, because the decision was welded to a store call. It is
  replaced by `TestOperationsOlderThan`, a five-case table over slice literals with **no mock,
  no fake, no store, no context**, including the boundary case (an operation stamped exactly at
  the cutoff is retained, because `Before` is strict).

  Two new tests keep an invariant that used to hold structurally: with one function returning
  both the dry-run count and the real count, their agreement was free. Now
  `TestDeleteOperations_CountMatchesInput` and `TestDeleteOperations_PartialCountOnError` pin
  that the reported count never exceeds the deletions actually made.
