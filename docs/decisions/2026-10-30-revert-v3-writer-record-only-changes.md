<!-- file: docs/decisions/2026-10-30-revert-v3-writer-record-only-changes.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7c1d5a3e-92b4-4f06-8e1a-5d3f0b6a9c27 -->
<!-- last-edited: 2026-10-30 -->

# Reverting the v3 Writer's record-only change types (OPS-WRITER-REVERT)

## Decision log

- Chosen: `ChangeTypeBookFileModify` and `ChangeTypeBookAuthorsModify` are declared in `internal/database` (beside `ChangeTypeBookFileRepoint`) and aliased in `opswriter`. `internal/undo` cannot import `opswriter` (the repoint constant moved for the same reason). Rejected: string literals in undo (two copies of the vocabulary).
- Chosen: no refusal branch existed to delete; both types fell through to `NotRestorableLabel`'s default. They now have validating cases like the repoint type.
- Chosen: book_file_modify writes through `ModifyBookFile` (the Writer's forward CAS path, already on `revertBookFileStore`), restoring one struct field by reflection on its JSON tag. An absent JSON side means the zero value. Rejected: the repoint revert's Get-then-`UpdateBookFile` (clobbers a concurrent write) and a whole-row map round trip (loses non-JSON fields).
- Chosen: rows revert independently; a drifted field is refused (`ReasonChangedSince`, counted Failed and ChangedSince) and the others revert. Reverse order comes from the plan's newest-first order; rows of one write share a timestamp but touch different fields.
- Chosen: book_authors_modify compares the current list with the journaled "after" normalised by `NormalizeBookAuthors` with the book's `AuthorID`, because the journal holds the callback's result while the store keeps the normalised list. The restore writes the journaled "before", which was read from the store and is already normalised.
- Chosen: every author the revert hands a credit back to must be a live author row (`CheckAuthorLive`, as the author-merge revert requires), in the preflight and the revert.
- Chosen: no author row is deleted. The Writer creates none; an author uncredited by the revert is left for `maintenance.purge-empty-authors`; an author created by the same operation has its own journaled create row and revert.
- Chosen: `user_data.go` classification is untouched; an unclassified type already reads as library data.
- Known limit: a `file_path` restored through book_file_modify does not hand the vacated path's single-owner key back (`reindexVacatedPath`); the forward ModifyBookFile did not claim one either. The store's replace-mode merge also keeps the AcoustID fingerprint and intro transcript when a revert would clear them to empty.
- Collision with PR #4312: the two `case` lines in `revertChangeIn` are in `internal/audiobooks/revert.go`, in a hunk #4312 does not touch. The version header there was bumped to 1.98.1 / 2026-10-30, identical to #4312's bump, so the header merges cleanly; the second PR to land needs no header change.
