<!-- file: PLAN.md -->
<!-- version: 1.0.0 -->
<!-- guid: 910a0be2-84ce-4711-aadc-b8a13d661ddb -->
<!-- last-edited: 2026-08-28 -->

# Metadata operation coalescing plan

## Goal

Safely merge compatible queued metadata operations so individually accepted
review actions do not create a long serial backlog, while never dropping work
or changing the semantics of an already-running operation.

## Constraints

- Merge only queued runs; a running run has already snapshotted its work.
- Union and stable-deduplicate `book_ids` only when every execution-affecting
  parameter matches (notably `write_back`).
- Return the existing operation id after a merge so the caller can still show
  an operation in the bell and cancellation remains meaningful.
- Preserve the current behavior for every operation definition that does not
  explicitly opt in.
- Prove no requested id is lost, incompatible params remain separate, and a
  running operation is never mutated.

## Steps

1. Add a narrow opt-in merge hook to the registry and a persistence-safe queued
   parameter update path.
2. Implement the hook for `metadata.batch-apply-cached` with stable unioning.
3. Add registry and server tests for safe merge, incompatible params, and the
   running-run boundary.
4. Run focused and package tests, add a changelog fragment, commit, push, open
   a draft PR, and merge only after green CI.
