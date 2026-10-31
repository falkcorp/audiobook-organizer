<!-- file: docs/decisions/2026-10-30-claim-lock-race.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2e09eb32-aeb8-4484-ab5c-69c195abb58a -->
<!-- last-edited: 2026-10-30 -->

# Hand-picked apply claims repair locks inside the commit's stripe hold

## Decision log

Chosen: the hand-picked metadata apply takes the book's field-state stripe
(`database.LockMetadataState`) BEFORE its `ModifyBook`, claims the repair locks of the
fields it changed straight after the commit, and releases the stripe before history is
recorded. Where the lock is already gone when the claim runs, it is taken again as the person's
(`OverrideLocked`, empty `LockSource`).

Rejected:

- Doing the lock write inside the `ModifyBook` callback or one Pebble batch with the value
  write. The documented lock order (`internal/database/metadata_field_lock_source.go`,
  `metadataStateLocks`) forbids taking a field-state stripe inside a book callback (two books
  can share stripes; the revert takes state first, then book). The revert itself shows the
  allowed shape: hold the state stripe across the book write
  (`internal/audiobooks/revert.go` ~2713-2725). "Inside the commit's hold" is read as that.
- Holding the stripe across history recording. Those writes are not part of the lock decision
  and the stripe says nothing slow runs under it.
- Re-lock fallback only, no stripe hold. It closes the lost lock, but the revert's paired
  value check could still see the lock as the repair's while the apply commits.
- Keeping a stripe-taking `ClaimRepairLocks` next to the new one. It would have no caller
  (deadcode ratchet), so it is replaced by `ClaimRepairLocksLocked` (caller holds the stripe,
  the `RecordUserOverridesLocked` convention).

## Write path trace

- Commit: `internal/metafetch/service_apply.go` `ApplyMetadataCandidateWithOptions` reads the
  locks in `guardedApplyWith` (`internal/metafetch/field_locks.go`, `claimed` = repair-locked
  keys the hand-picked candidate may write), commits via `commitApply`
  (`internal/metafetch/apply_history.go`: `ModifyBook` + `database.MergeBookChanges`, then history),
  and used to call `database.ClaimRepairLocks` after that returned, stripe released.
- Claim: `internal/database/metadata_field_lock_source.go`, formerly `ClaimRepairLocks`, took the
  stripe itself, cleared `LockSource` only on rows still `IsRepairLock()`, and ignored the rest.
- Revert: `internal/audiobooks/revert.go` `revertFieldLock` (~1224) takes the same stripe and
  lifts the lock (`OverrideLocked=false`, `LockSource=""`) after `undo.CheckFieldLockCurrent`
  (`internal/undo/field_lock.go:76`); `revertMetadataUpdate` (~2713) holds the stripe across the
  paired-lock check and the value compare-and-set.
- Two windows: (1) lift between the apply's lock read and the commit; (2) lift between the commit
  and the claim. Either left the new value unlocked. The stripe hold closes (2) outright; the
  re-lock fallback closes (1), which a stripe taken only at commit time cannot.

## Hazard found while designing the re-lock

`database.MergeBookChanges` copies only the columns the apply changed; the rest keep whatever a
concurrent writer committed. The old test for "this apply changed the field" compared `before`
with the COMMITTED row, so a revert that put the repair's old value back would read as changed,
and a re-lock would have locked a value the person never chose. `handPickedChangedLocks` now
requires the apply's own working copy to differ from `before` as well as the committed row.

## Known limit

A person's own unlock landing in window (1) looks the same as a revert's lift and is re-locked.
The window is one apply long and the person just picked that value.

## Verification note

`TestApplyMetadataCandidate_RevertLiftBeforeCommitRelocksAsPerson` is deterministic and fails
without the fallback. `..._RevertLiftAfterCommitWaitsForTheClaim` fails without the stripe hold
(checked by mutation) but its negative wait is a 150 ms timeout, so the failing direction depends
on scheduling; the passing direction does not.
