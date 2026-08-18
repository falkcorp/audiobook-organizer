### Changed

#### `OperationStore`, `OpsV2Store` and `TagStore` split into focused interfaces

Three of the widest interfaces in `internal/database` are now assembled from
small, named pieces instead of being one flat list:

- **`OperationStore`** (30 methods, 19 referencing files) → 7 interfaces of 3–5:
  lifecycle, reads, resumable state, per-entity changes, logs, results, pruning.
- **`OpsV2Store`** (32 methods, 13 referencing files) → 8 interfaces of 2–6:
  definitions, lifecycle, queue, state/checkpoints, observability, dependency
  revisions, completions, batch buckets.
- **`TagStore`** (27 methods, 6 referencing files) → 6 interfaces of 4–5. The
  seam here was already latent in the naming: the same nine operations repeated
  three times for books, authors and series, now split reader/writer per entity.

Each original name is retained as the composition of its pieces, so every method
set is byte-identical and no consumer moves. Verified per interface by diffing
the method names before and after (27→27, 30→30, 32→32, all identical) and
independently by the type checker. Mocks are unaffected — `mockery` regenerates
to no diff.

All three compositions land at 6–8 embeds, under the width threshold, so none
needs an override. `interfacebloat` violations in `internal/database`: 14 → 11.
