The purge's undo hold now covers every undo that needs a trashed book's row, not
only combines. Three kinds hold a soft-deleted book: a combine journal (as
before); an unreverted, unvoided `book_soft_delete` row in the operation ledger
(fs-regroup-xml fragments, retire-into and the folder-books retire soft-delete a
shell and journal it; reverting the operation restores it, and the revert
refuses "no longer exists" for a purged one, leaving the operation half
reverted), decided with the revert's own stamp check
(`undo.CheckSoftDeleteCurrent`); and an open auto-merge journal entry (the loser
of a `dedup.auto-resolve` merge that `UnmergeAuto` has not reversed, provisional
entries included), re-read when a new entry is journaled mid-pass. Every
decision is made under the merge lock. Any record the hold cannot read keeps the
book and refuses the pass. The ledger lookup uses the per-book index
(`opchange_by_book:`); until `maintenance.opchange-book-index-rebuild` has built
and verified it this boot, every purge path (bulk, nightly, single, discard)
refuses with `ErrUndoLedgerNotReady` instead of scanning the whole ledger once
per trashed book. Cost: ledger, combine and auto-merge holds last until the undo
runs (none of those records is pruned), so held shells stay in the trash and
older fs-regroup shells now show "kept: undo possible".

`UnmergeAuto` checks that the loser (and the winner, when it was modified)
still exists before it touches any sibling, and refuses with "nothing reverted"
when the loser was purged; it used to move the siblings first and then stop.

A purge that stops early (cancelled, timed out, or refused) now says so:
`stopped_early` and `stop_reason` in the result, "STOPPED EARLY" in the summary,
the result stored for the cancelled or failed operation too, and a final
progress count that matches the books actually decided.
