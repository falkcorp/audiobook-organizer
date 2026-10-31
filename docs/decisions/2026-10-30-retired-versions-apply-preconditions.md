<!-- file: docs/decisions/2026-10-30-retired-versions-apply-preconditions.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4d1c7a9e-2b53-4f86-a0e7-6c9b13d58f24 -->
<!-- last-edited: 2026-10-30 -->

# Retired-versions Apply: Writer journaling, combine link, version-delete guard

TODO entry: TODO-RETIRED-APPLY-PRECONDITIONS, items 1, 2 and 9.

## Decision log

### Item 1: the fixer journals through repairs.Writer

Chosen: replace the `retireWriter` methods `CreateRetiredVersion` and `RetireRows` with one
`RetireVersion(repairs.RetireSpec)` that calls `Writer.RetireVersion`, and `RehomeVersion`
with a call to `Writer.RehomeRetiredVersion`. The version and its rows are one Writer call, so
the rows are the commit point as before. `RecordPlan`, `ReassignExternalID` and `SetMergedInto`
stay in the fixer's adapter: the Writer has no equivalents and its own doc leaves the
external-id and merged-into rows to the caller. The fixer-local version creation, row
journaling, `retiredRowStore`, `retireRow` and the store capability lookup are deleted.

`RetireSpec` gained `SourceOriginalPath`, `MetadataJSON` and `IngestDate` (a zero value means
the retire time, as `IngestDate` did for the Writer before). Without them the retired version
would have lost the provenance the fixer wrote into `MetadataJSON` and the shell's creation
time as `IngestDate`.

`Writer.CheckRetireSupport` is new and is called when the fixer builds its writer: the old
adapter checked for `RetiredBookFileWriter` up front, before the plan record was journaled, and
the Writer only discovers a missing capability inside `RetireVersion`, after the plan record.
The check keeps the order and runs against the Writer's own store.

Rejected:

- Keep the old adapter and only mirror the Writer's behaviour. That is the duplicate the TODO
  item wanted deleted.
- Fold the external-id and merged-into steps into the Writer as well. Outside the item, and the
  Writer's doc already assigns them to the caller.

### Item 2: the combine link

Chosen: `retiredDetail` carries the sorted ids of the unreversed combine journals that name the
shell (the same list that sets the `combine_undo_aware` flag). Apply reads it from the
under-lock re-check, not the plan, and passes the id as `RetireSpec.CombineJournalID`. The
planned fingerprint already includes the journal ids, so a changed set is `changed_since_plan`.
Both holds (at Replan and at Apply) are gone; `holdCombineLink` and
`retCatCombineLinkPending` are deleted.

A shell named by MORE THAN ONE unreversed journal stays held, under a new narrow kind
`combine_undo_multi_journal` (retry_later), at Replan and at Apply. `RetireSpec` takes one
combine id, so linking one of two would leave the other combine's undo blind to the retire,
which is the silent non-undoable case the link exists to prevent. It clears when all but one of
those combines is undone.

Rejected:

- Link the first or last journal id. Loses the other combine's undo.
- Extend `RetireSpec` to a list of combine ids. `RetireSpec`, the `RetirePlace` row format and
  `UndoCombine`'s link scan all assume one; changing the journaled row format is an owner call.
- Fail the multi-journal row as an error. It is transient (the owner can undo one combine), so
  retry_later fits.

### Item 9: the version-delete guard already existed

The brief asked for the revert of a `book_version_create` row to refuse while the version
still holds rows. That guard is already in the tree, in the revert service rather than the
repairs Writer (the Writer has no revert path): `RevertService.revertBookVersionCreate`
deletes through `DeleteBookVersionIf`, which refuses with `database.ErrRetiredVersionHasFiles`
under the version stripe whenever any retired row under the holder carries the version id,
whichever op journaled the row, and the revert maps that to
`*undo.ReferentError{Reason: ReasonDependentNotReverted}`. A refused row is counted in
`RevertResult.Failed`, the other rows of the operation still revert, and the call returns
`partially reverted`. Every other path that reverts a `book_version_create` row goes through
the same function; `DeleteBookVersion` has no other production caller on a retired version.

Chosen: add no second guard. Add the missing test legs in `internal/audiobooks`: refused while
rows remain (typed, version survives), refused after one of two rows is reverted, succeeds once
all retire rows are reverted; and a row journaled by another operation keeps the version
alive, the first operation's other rows still revert, and a repeat after the other operation is
reverted deletes the version.

Rejected: a duplicate check in the Writer or the undo plan. It would restate the store's
check, under no lock, with a window between check and delete.

## Findings that differ from the brief

- Item 9 was already done (above).
- The brief's test for item 2 asks that UndoCombine reverse a fixer retire first. Today
  `UndoCombine` (`internal/merge/combine_journal.go`, the "has since been given N file(s)"
  precondition) refuses a first-attempt undo when the absorbed shell holds any row the combine
  did not move, including a row a linked retire took ("taken by a retire since, still held by
  it"). A real combine empties its shell, so a shell that is a retire candidate has necessarily
  gained its rows since, and the undo of such a combine is refused whether or not the retire
  is linked. The end-to-end reversal of a linked retire is covered, for a retire that took no
  gained row, by `TestRevertCombine_EndToEndReversesALinkedRetireUnderTheMergeLock`. The new
  fixer test pins what this change controls: the link and the id in every retire row exist,
  UndoCombine finds the retire through them (it names the row as retired by a linked retire)
  and refuses without changing anything, and reverting the retire op puts the row back. Whether
  `UndoCombine` should reverse a linked retire of gained rows instead of refusing is a merge
  decision left to the owner.
- A shell that a combine emptied (no live rows) is classed `no_book_files` and never reaches the
  fixer's retire, so in practice a `combine_undo_aware` row that applies is one whose rows
  arrived after the combine.
