// file: internal/merge/locked_helpers_test.go
// version: 1.0.0
// guid: 5c0e8a47-2d16-4b93-9a7f-31e6d4b8c205
// last-edited: 2026-10-26

package merge

import "github.com/falkcorp/audiobook-organizer/internal/database"

// Production callers take the merge lock themselves (TryLockMergeRMWWithin:
// an interactive handler bounds its wait for a running combine and keeps the
// hold for the whole action) and call the ...Locked body. These helpers do the
// same with the unbounded lock, so a test exercises the body exactly as a
// caller reaches it.

// restoreFromTrash runs RestoreFromTrashLocked under one hold of the merge
// lock.
func restoreFromTrash(store TrashRestoreStore, id string, apply func(*database.Book), judge func(before, after *database.Book) error, handOff func(before, after *database.Book), joinGroups ...string) (TrashRestoreResult, error) {
	LockMergeRMW()
	defer UnlockMergeRMW()
	return RestoreFromTrashLocked(store, id, apply, judge, handOff, joinGroups...)
}

// discardUserStateThenHardDelete runs DiscardUserStateThenHardDeleteLocked
// under one hold of the merge lock.
func discardUserStateThenHardDelete(db UserProgressMerger, bookID string, precheck, del func() error) (DiscardedUserState, error) {
	LockMergeRMW()
	defer UnlockMergeRMW()
	return DiscardUserStateThenHardDeleteLocked(db, bookID, precheck, del)
}

// hardDeleteKeepingUserState runs HardDeleteKeepingUserStateLocked under one
// hold of the merge lock.
func hardDeleteKeepingUserState(db UserProgressMerger, books VersionGroupReader, book *database.Book, precheck, del func() error) (string, error) {
	LockMergeRMW()
	defer UnlockMergeRMW()
	return HardDeleteKeepingUserStateLocked(db, books, book, precheck, del)
}
