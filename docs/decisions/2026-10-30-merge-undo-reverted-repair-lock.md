<!-- file: docs/decisions/2026-10-30-merge-undo-reverted-repair-lock.md -->
<!-- version: 1.0.0 -->
<!-- guid: ab821a62-b070-4c81-b0ec-e9775455ddfb -->
<!-- last-edited: 2026-10-30 -->

# MERGE-UNDO-STRIPE: reverted repair locks in the combine undo

## Finding that contradicts the brief

Part 1 (take the stripe) was already in place on main. `restoreOverrideLocks`
(`internal/merge/combine_journal.go`, the only field-state restore in the
merge package) takes `defer database.LockMetadataState(survivorID)()` before
its read and holds it across every Upsert/Delete. The TODO's line numbers
(~994-1003) were stale. No production change was needed for part 1; a race
test (`TestRestoreOverrideLocks_StripeAgainstClaimRepairLocks`) now pins it.
It mirrors `repairs.Writer.LockFields` / `ClaimRepairLocks`, which use the
same `LockMetadataState` stripe.

## Reverted-operation check (part 2)

A repair lock source is `database.RepairLockSource(opID)` = `repair:<op>`
(`internal/database/metadata_field_lock_source.go:37`, parsed by
`RepairLockOp` at line 40). The repair journals its lock as a
`field_lock` OperationChange (`internal/repairs/writer_locks.go:103`), and the
operation revert lifts the lock in `audiobooks.RevertService.revertFieldLock`
(`internal/audiobooks/revert.go:1220`), stamping RevertedAt on that row. The
check is: `GetBookChanges(book)` has a row with ChangeType `field_lock`, the
same OperationID, BookID and FieldName, not Voided, RevertedAt != nil
(`dropRevertedRepairLock`, `internal/merge/combine_journal.go`).

## Decisions

- Lift the lock entirely (OverrideLocked=false, LockSource="") rather than
  only clearing LockSource. Alternative rejected: clear only the source, which
  would turn a reverted repair's lock into a permanent person's lock.
- No field_lock row found, or only a voided row: keep the lock as journaled
  (nothing proves a revert). Alternative rejected: treating "no row" as
  reverted, which would drop locks whose journal rows were pruned.
- Journal read failure: keep the lock and add a warning; never fail the undo.
- Added `GetBookChanges` to the merge package's `combineReverseMappingStore`
  (already on database.Store, so no mock regeneration). Alternative rejected:
  a type-assertion capability; the repo prefers explicit narrow interfaces.
- `internal/undo` imports `internal/merge`, so the `field_lock` string is a
  local constant (`fieldLockChangeType`) instead of `undo.ChangeTypeFieldLock`.
- Only restoreOverrideLocks' restored rows are checked; this is the only
  place the merge undo re-installs a lock row.
