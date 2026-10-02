// file: internal/merge/trash_restore.go
// version: 1.0.0
// guid: 59c79d7e-300f-40f3-aec2-d7d290bdb7ab
// last-edited: 2026-10-01

package merge

import (
	"fmt"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
)

// TrashRestoreStore is what RestoreFromTrash reads and writes.
type TrashRestoreStore interface {
	versionprimary.EnsureStore
}

// TrashRestoreResult is one RestoreFromTrash.
type TrashRestoreResult struct {
	// Book is the row as written, or as read when nothing was written. Nil
	// when the book does not exist.
	Book *database.Book
	// Before is the stored row as the write read it, under the book's write
	// lock (for a caller's history diff). Zero when no write ran.
	Before database.Book
	// Restored is true when the row was in the trash and this call restored it.
	Restored bool
	// RedirectFrom is the book the restored row's sync identity redirected
	// to (its merge survivor), when the restore removed that redirect.
	RedirectFrom string
}

// syncRedirectClearer removes a book's merge redirect whatever its target
// (database.PebbleStore.ClearSyncRedirect).
type syncRedirectClearer interface {
	ClearSyncRedirect(loserBookID string) (winnerBookID string, cleared bool, err error)
}

// pendingRepairScanner is the raw-key surface for the pending user-state
// repair records a restored loser leaves behind.
type pendingRepairScanner interface {
	ScanPrefix(prefix string) ([]database.KVPair, error)
	DeleteRaw(key string) error
}

// RestoreFromTrash is the user-facing restore of one book (the single restore,
// the bulk "restore" action and a batch update setting
// marked_for_deletion=false). Owner decision 2026-10-01: a book restored from
// the trash is a book of its own again.
//
// When the row is in the trash (database.IsInTrash) it:
//
//  1. removes the sync-identity redirect a merge left on it (RecordSyncMerge),
//     so ABS renders it as its own item instead of dropping it as a merge
//     loser. A combine's absorbed shell and a MergeBooks loser never record
//     MergedIntoBookID, so the redirect is read from the sync layer, not
//     from that column. Done first: the clear is idempotent, while a second
//     restore of a row already written live is a no-op, so a clear that
//     failed after the write could never be retried;
//  2. under the book's version-group lock, reads the group's incumbent and
//     writes database.RestoreBookFromTrash plus versionprimary.YieldToIncumbent
//     in one ModifyBook;
//  3. drops the pending user-state repair records that would move the
//     restored book's state onto its old survivor.
//
// User state (progress, positions, bookmarks) the merge already followed onto
// the survivor stays there; only the identity redirect is removed.
//
// A row that is not in the trash is not restored: no redirect change, no
// yield, nothing written, unless apply is set. apply, when set, runs inside the
// same ModifyBook after the restore (a batch update's other fields), and the
// row is written whether or not it was in the trash.
//
// It takes LockMergeRMW, so it never interleaves with a merge or combine that
// is recording a redirect on the same book. The caller hands off the group's
// primary (EnsureSinglePrimary) after it returns, when Restored is set.
//
// There is no operation journal for a trash restore (there is none for the
// trash either); the redirect removal and every dropped record are logged.
func RestoreFromTrash(store TrashRestoreStore, id string, apply func(*database.Book)) (TrashRestoreResult, error) {
	var res TrashRestoreResult
	LockMergeRMW()
	defer UnlockMergeRMW()

	book, err := store.GetBookByID(id)
	if err != nil {
		return res, fmt.Errorf("read %s: %w", id, err)
	}
	if book == nil {
		return res, nil
	}
	trashed := database.IsInTrash(book)
	if !trashed && apply == nil {
		res.Book = book
		return res, nil
	}

	var files []database.BookFile
	var env database.TrashRestoreEnv
	if trashed {
		// The file rows decide whether the row may come back "organized"
		// (database.RestoreLibraryStateFromTrash).
		if files, err = store.GetBookFiles(id); err != nil {
			return res, fmt.Errorf("read files of %s: %w", id, err)
		}
		env = TrashRestoreEnv()
		if res.RedirectFrom, err = clearRestoredRedirect(store, id); err != nil {
			return res, err
		}
	}

	gid := ""
	if trashed && book.VersionGroupID != nil {
		gid = strings.TrimSpace(*book.VersionGroupID)
	}
	incumbent := ""
	unlockGroup := func() {}
	if gid != "" {
		// The incumbent is read and the yield written under the group's lock,
		// so no hand-off can crown or demote a member in between. Writers that
		// set a flag without a hand-off (a batch is_primary_version edit, the
		// scanner) do not take this lock; for those, the EnsureSinglePrimary
		// the caller runs afterwards is what leaves the group with one primary.
		unlockGroup = versionprimary.LockGroup(gid)
		if incumbent, err = versionprimary.IncumbentExcept(store, gid, id); err != nil {
			unlockGroup()
			restoreRedirect(store, id, res.RedirectFrom)
			return res, err
		}
	}
	updated, err := store.ModifyBook(id, func(row *database.Book) error {
		res.Before = *row
		if trashed && database.RestoreBookFromTrash(row, files, env) {
			res.Restored = true
			// Yield only in the group whose incumbent was read; a row moved to
			// another group since is left to the caller's hand-off.
			if gid != "" && row.VersionGroupID != nil && strings.TrimSpace(*row.VersionGroupID) == gid {
				versionprimary.YieldToIncumbent(row, incumbent)
			}
		}
		if apply != nil {
			apply(row)
			return nil
		}
		if !res.Restored {
			return database.ErrSkipBookWrite
		}
		return nil
	})
	unlockGroup()
	if err != nil || updated == nil {
		restoreRedirect(store, id, res.RedirectFrom)
		res.Restored = false
		if err != nil {
			return res, fmt.Errorf("restore %s: %w", id, err)
		}
		return res, nil
	}
	res.Book = updated
	if res.Restored {
		dropRestoredPendingRepairs(store, id)
	}
	return res, nil
}

// clearRestoredRedirect removes book id's merge redirect and returns the book
// it pointed at ("" when there was none, or the store has no sync layer).
func clearRestoredRedirect(store any, id string) (string, error) {
	clearer, ok := database.AsCapability[syncRedirectClearer](store)
	if !ok {
		return "", nil
	}
	winner, cleared, err := clearer.ClearSyncRedirect(id)
	if err != nil {
		return "", fmt.Errorf("clear sync redirect of %s: %w", id, err)
	}
	if !cleared {
		return "", nil
	}
	mlog.Info("restore from trash: removed the sync redirect book=%s -> survivor=%s; the book is its own ABS item again (progress followed onto the survivor stays there)",
		logger.SanitizeLogValue(id), logger.SanitizeLogValue(winner))
	return winner, nil
}

// restoreRedirect puts back a redirect clearRestoredRedirect removed, when the
// restore that removed it did not write. Best-effort: the row is still in the
// trash, and a merge loser in the trash with no redirect is what a merge whose
// follow failed leaves too; the failure is logged at Error.
func restoreRedirect(store any, id, winner string) {
	if winner == "" {
		return
	}
	ids := database.AsSyncIdentityStore(store)
	if ids == nil {
		return
	}
	if err := ids.RecordSyncMerge(id, winner); err != nil {
		mlog.Error("restore from trash failed and the sync redirect book=%s -> survivor=%s could not be put back: %s",
			logger.SanitizeLogValue(id), logger.SanitizeLogValue(winner), logger.SanitizeLogValue(fmt.Sprint(err)))
	}
}

// dropRestoredPendingRepairs deletes the pending user-state repair records
// whose loser is the restored book. Each one is a move onto the old survivor
// still owed; the book is its own again, so the move is no longer owed, and
// left in place the sweep would drain the book onto the survivor the next time
// it is trashed. Records of a book-ID change (BookIDChange) are not merges and
// are kept. Failures are logged at Error and do not fail the restore, which
// is written: while the book is live the sweep defers such a record
// (ErrPendingLoserLive), so it moves nothing in the meantime.
func dropRestoredPendingRepairs(store any, id string) {
	kv, ok := database.AsCapability[pendingRepairScanner](store)
	if !ok {
		return
	}
	recs, undecodable, err := ListPendingUserStateRepairs(kv)
	if err != nil {
		mlog.Error("restore from trash: list pending user-state repairs for book=%s: %s",
			logger.SanitizeLogValue(id), logger.SanitizeLogValue(fmt.Sprint(err)))
		return
	}
	if len(undecodable) > 0 {
		mlog.Warn("restore from trash: %d undecodable pending user-state repair record(s) not checked for book=%s",
			len(undecodable), logger.SanitizeLogValue(id))
	}
	for _, rec := range recs {
		if rec.LoserBookID != id || rec.BookIDChange {
			continue
		}
		key := pendingRepairKey(rec.LoserBookID, rec.WinnerBookID)
		if err := kv.DeleteRaw(key); err != nil {
			mlog.Error("restore from trash: pending user-state repair %s not dropped: %s",
				logger.SanitizeLogValue(key), logger.SanitizeLogValue(fmt.Sprint(err)))
			continue
		}
		mlog.Info("restore from trash: dropped pending user-state repair book=%s -> survivor=%s",
			logger.SanitizeLogValue(id), logger.SanitizeLogValue(rec.WinnerBookID))
	}
}
