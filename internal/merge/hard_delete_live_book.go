// file: internal/merge/hard_delete_live_book.go
// version: 1.0.0
// guid: 4e7b1c92-6a30-4d85-b1f7-0c93d8a25e61
// last-edited: 2026-10-29

package merge

import (
	"context"
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
)

// HardDeleteLiveBookLocked is the one hard delete of a book that is NOT in the
// trash, for every caller that asks for it: the single-book delete
// (audiobooks.DeleteAudiobook) and the bulk one (batch.hardDeleteLocked). It
// exists because the two were written separately and drifted: until 2026-10-29
// the batch path never handed a deleted primary's flag on, and for ten days
// before 2026-10-25 it had no owned-files check at all, while the single path
// had both. What a hard delete does around DeleteBook is decided here, once.
//
// In order, all under the merge lock the caller holds (TryLockMergeRMWWithin):
//
//  1. carries the users' listening state to a version Audiobookshelf lists and
//     runs del, or refuses (HardDeleteKeepingUserStateLocked:
//     ErrBookHasListeningState). Its precheck, which runs under this hold
//     before the carry moves anything (or before del when there is no state),
//     refuses a book that still owns book_file rows (database.CheckBookOwnsNoFiles,
//     the question DeleteBook asks: retired rows included) and a retire shell
//     (database.RefuseRetiredShell). DeleteBook refuses both too, but only after
//     the carry, which would already have moved the state off a book that is
//     then kept. The caller may make the same check earlier, before it takes the
//     lock, so that a refused book never waits for it; this one is the one that
//     cannot be raced by a merge;
//  2. after a successful delete, hands the flag of the group book named on to
//     another member (versionprimary.EnsureSinglePrimary), so a hard-deleted
//     primary does not leave its group with none. Best-effort: the delete has
//     committed, a hand-off that fails is logged and left for
//     version-group-primary-repair.
//
// Not here, on purpose: external-ID tombstones and the tombstone snapshot. A
// live book's hard delete has never written them (DeleteBook does not); only the
// purge of a trashed book does, and both callers send a trashed book to the
// purge (audiobooks purgeNowLocked), not here.
//
// book is the row read before the delete, so it still names its group after the
// row is gone. del is the caller's own delete (the single path blocks the book's
// hash inside it); it must call DeleteBook. A refusal of the precheck is
// returned as its own error, not wrapped (PrecheckCause is already applied);
// every other error is HardDeleteKeepingUserStateLocked's, for the caller to
// word. carriedTo names the version the state was carried to
// ("" when there was none).
func HardDeleteLiveBookLocked(store versionprimary.EnsureStore, book *database.Book, rootDir string, del func() error) (carriedTo string, err error) {
	if book == nil || book.ID == "" {
		return "", fmt.Errorf("%w: no book", ErrUserStateCheckFailed)
	}
	refuse := func() error {
		if err := database.CheckBookOwnsNoFiles(store, book.ID, "hard delete"); err != nil {
			return err
		}
		// A retire shell owns no live row, so the check above passes it, but
		// deleting it would drop the record of where its retired versions came
		// from. The retire repair takes the merge lock too, so this run under
		// the lock catches a shell retired since the caller's first look.
		return database.RefuseRetiredShell(store, book.ID, "hard delete")
	}
	// A retire shell is refused before the store is asked anything else, in its
	// own words, whether or not the store could carry state; the precheck below
	// asks again under the same hold, right before anything moves.
	if err := database.RefuseRetiredShell(store, book.ID, "hard delete"); err != nil {
		return "", err
	}
	merger, ok := database.AsCapability[UserProgressMerger](store)
	if !ok {
		return "", fmt.Errorf("hard delete %s: the store cannot check or carry users' listening state, so the delete is refused", book.ID)
	}
	carriedTo, err = HardDeleteKeepingUserStateLocked(merger, store, book, refuse, del)
	if err != nil {
		if cause := PrecheckCause(err); cause != nil {
			// Refused under the merge lock with nothing moved and del not run:
			// the check's own words, not the merge package's wrapper.
			return "", cause
		}
		return carriedTo, err
	}
	if gid := groupOfBook(book); gid != "" {
		if _, herr := versionprimary.EnsureSinglePrimary(context.Background(), store, gid,
			versionprimary.Env{RootDir: rootDir}); herr != nil {
			mlog.Warn("hard delete of %s: the primary hand-off in version group %s failed: %v",
				logger.SanitizeLogValue(book.ID), logger.SanitizeLogValue(gid), herr)
		}
	}
	return carriedTo, nil
}

func groupOfBook(b *database.Book) string {
	if b == nil || b.VersionGroupID == nil {
		return ""
	}
	return *b.VersionGroupID
}
