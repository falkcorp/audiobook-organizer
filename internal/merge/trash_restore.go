// file: internal/merge/trash_restore.go
// version: 1.3.0
// guid: 59c79d7e-300f-40f3-aec2-d7d290bdb7ab
// last-edited: 2026-10-01

package merge

import (
	"context"
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
	// to (its merge survivor), when the restore removed that redirect. Empty
	// when there was none, or it was kept (the row is not ABS-listable).
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
//  1. under the book's version-group lock, reads the group's incumbent and
//     works out the row the restore will write (database.RestoreBookFromTrash
//     plus versionprimary.YieldToIncumbent);
//  2. when that row will be listed by ABS as an item of its own, with audio
//     to play (database.RestoredRowIsABSListable, and not left in a group
//     whose live incumbent the caller's hand-off keeps), removes the sync-identity
//     redirect a merge left on it (RecordSyncMerge), so ABS renders it instead
//     of dropping it as a merge loser. A combine's absorbed shell and a
//     MergeBooks loser never record MergedIntoBookID, so the redirect is read
//     from the sync layer, not from that column. Done before the write: the
//     clear is idempotent, while a second restore of a row already written
//     live is a no-op, so a clear that failed after the write could never be
//     retried;
//  3. writes that restore in one ModifyBook, releases the group lock, and
//     hands off the primary of the row's version group (handOff, or by
//     default versionprimary.EnsureSinglePrimary on its group before and
//     after the write);
//  4. re-reads the row, now carrying the flags the hand-off left, and puts the
//     redirect back, or removes it, to match (reconcileRestoredRedirect);
//  5. when the redirect is gone, drops the pending user-state repair records
//     that would move the restored book's state onto its old survivor.
//
// The redirect is KEPT when the restored row will not be ABS-listable
// (re-review of #3649, findings 1 and 2). The redirect is what forwards a
// client's old libraryItemId, and the progress the merge followed onto the
// survivor, to the survivor. Removing it from a row ABS does not list strands
// that client: a combine shell comes back "imported" with no files, and a
// MergeBooks or fragment-fixer loser sits in the survivor's version group
// with IsPrimaryVersion=false, so ABS hides it while its old id would stop
// forwarding and the client would see its progress reset. Its pending repair
// records are kept with it: while the book is live the sweep defers them
// (ErrPendingLoserLive), and they are still owed if it is trashed again.
//
// The hand-off runs here, not in the callers, because it decides the flag the
// redirect decision reads. Until the second re-review of #3649 every caller
// handed off after this returned, so a loser whose survivor was gone too kept
// its redirect (not primary at decision time) and was then crowned: ABS listed
// it and still forwarded its id elsewhere. The hand-off takes the group lock
// itself, so it runs after this call releases it, still under LockMergeRMW;
// the decision then reads the row the hand-off wrote.
//
// Known gap (documented, not built): an organized row whose files are all
// Missing keeps its redirect (no present file), and a later repoint that finds
// its audio does not remove it.
//
// User state (progress, positions, bookmarks) the merge already followed onto
// the survivor stays there; only the identity redirect is removed.
//
// A row that is not in the trash is not restored: no redirect change, no
// yield, nothing written, unless apply is set. apply, when set, runs inside the
// same ModifyBook after the restore (a batch update's other fields), and the
// row is written whether or not it was in the trash.
//
// handOff, when set, replaces the default hand-off and runs after every write
// (restore or apply), with the row as the write read it and as written; a
// batch update passes its own, which honours an is_primary_version or
// version_group_id in the same payload. Callers must not hand off again
// afterwards: a flag change after the redirect decision is the gap this
// closes.
//
// It takes LockMergeRMW, so it never interleaves with a merge or combine that
// is recording a redirect on the same book.
//
// There is no operation journal for a trash restore (there is none for the
// trash either); the redirect removal and every dropped record are logged.
func RestoreFromTrash(store TrashRestoreStore, id string, apply func(*database.Book), handOff func(before, after *database.Book)) (TrashRestoreResult, error) {
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
		// (database.RestoreLibraryStateFromTrash) and whether it keeps its
		// merge redirect (database.RestoredRowIsABSListable).
		if files, err = store.GetBookFiles(id); err != nil {
			return res, fmt.Errorf("read files of %s: %w", id, err)
		}
		env = TrashRestoreEnv()
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
			return res, err
		}
	}
	// restoreRow is the restore the write applies; the redirect decision runs
	// it on a copy first, so both see the same rule.
	restoreRow := func(row *database.Book) bool {
		if !database.RestoreBookFromTrash(row, files, env) {
			return false
		}
		// Yield only in the group whose incumbent was read; a row moved to
		// another group since is left to the caller's hand-off.
		if gid != "" && row.VersionGroupID != nil && strings.TrimSpace(*row.VersionGroupID) == gid {
			versionprimary.YieldToIncumbent(row, incumbent)
		}
		return true
	}
	// listable is the redirect decision. A row that stays in a group with a
	// live incumbent ends non-primary whatever its flag says: the caller's
	// hand-off keeps the incumbent, and a nil flag, which YieldToIncumbent
	// leaves alone and ABSLibraryFilter reads as primary, is written false by
	// it. Judged on the flag alone, such a row lost its redirect and was then
	// hidden.
	listable := func(row *database.Book) bool {
		if incumbent != "" && row.VersionGroupID != nil && strings.TrimSpace(*row.VersionGroupID) == gid {
			return false
		}
		return database.RestoredRowIsABSListable(row, files, env)
	}
	if trashed {
		// RestoreBookFromTrash and YieldToIncumbent assign fresh pointers and
		// never write through the row's, so a shallow copy leaves book alone.
		preview := *book
		restoreRow(&preview)
		if listable(&preview) {
			if res.RedirectFrom, err = clearRestoredRedirect(store, id); err != nil {
				unlockGroup()
				return res, err
			}
		} else {
			mlog.Info("restore from trash: book=%s will not be listed by ABS as an item of its own (not primary, not organized, or no present file in the library folder); any merge redirect is kept so its old id still reaches the survivor",
				logger.SanitizeLogValue(id))
		}
	}
	updated, err := store.ModifyBook(id, func(row *database.Book) error {
		res.Before = *row
		if trashed && restoreRow(row) {
			res.Restored = true
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
		res.RedirectFrom = ""
		res.Restored = false
		if err != nil {
			return res, fmt.Errorf("restore %s: %w", id, err)
		}
		return res, nil
	}
	res.Book = updated
	before := res.Before
	switch {
	case handOff != nil:
		handOff(&before, updated)
	case res.Restored:
		handOffRestoredGroups(store, &before, updated, env.RootDir)
	}
	if !res.Restored {
		return res, nil
	}
	// The decision reads the row as the hand-off left it, not as written.
	final, err := store.GetBookByID(id)
	if err != nil || final == nil {
		mlog.Error("restore from trash: re-read of book=%s after the primary hand-off failed (%v); deciding its redirect on the row as written",
			logger.SanitizeLogValue(id), err)
		final = updated
	}
	if reconcileRestoredRedirect(store, &res, final, func(b *database.Book) bool {
		return database.RestoredRowIsABSListable(b, files, env)
	}) {
		dropRestoredPendingRepairs(store, id)
	}
	return res, nil
}

// handOffRestoredGroups is the default hand-off after a restore: one live
// primary in the row's version group, and in the group it was in when the
// write read it if that differs. Best-effort: the restore has committed; a
// failed hand-off is logged.
func handOffRestoredGroups(store TrashRestoreStore, before, after *database.Book, rootDir string) {
	seen := map[string]bool{}
	for _, b := range []*database.Book{after, before} {
		if b == nil || b.VersionGroupID == nil {
			continue
		}
		gid := strings.TrimSpace(*b.VersionGroupID)
		if gid == "" || seen[gid] {
			continue
		}
		seen[gid] = true
		if _, err := versionprimary.EnsureSinglePrimary(context.Background(), store, gid,
			versionprimary.Env{RootDir: rootDir}); err != nil {
			mlog.Warn("restore from trash: primary hand-off in version group %s after restoring %s failed: %v",
				logger.SanitizeLogValue(gid), logger.SanitizeLogValue(after.ID), err)
		}
	}
}

// reconcileRestoredRedirect re-runs the redirect decision on final, the row as
// the restore and its primary hand-off left it, which the hand-off, the
// caller's apply or a concurrent writer may have left different from the
// preview the decision ran on. A redirect removed
// for a row that is not listable after all is put back; a row that turned
// out listable has its redirect removed now. res.RedirectFrom is kept in step.
// It reports whether the row is listable and no redirect is left on it, i.e.
// whether the pending moves onto the old survivor are no longer owed.
func reconcileRestoredRedirect(store any, res *TrashRestoreResult, final *database.Book, listable func(*database.Book) bool) bool {
	id := final.ID
	if !listable(final) {
		if res.RedirectFrom != "" {
			mlog.Warn("restore from trash: book=%s is not ABS-listable after the restore and hand-off; putting its sync redirect to survivor=%s back",
				logger.SanitizeLogValue(id), logger.SanitizeLogValue(res.RedirectFrom))
			restoreRedirect(store, id, res.RedirectFrom)
			res.RedirectFrom = ""
		}
		return false
	}
	if res.RedirectFrom != "" {
		return true
	}
	winner, err := clearRestoredRedirect(store, id)
	if err != nil {
		mlog.Error("restore from trash: book=%s is restored and ABS-listable but its sync redirect was not removed, so ABS keeps dropping it: %s",
			logger.SanitizeLogValue(id), logger.SanitizeLogValue(fmt.Sprint(err)))
		return false
	}
	res.RedirectFrom = winner
	return true
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
// restore that removed it did not write, or wrote a row that is not
// ABS-listable after all. Best-effort: a merge loser with no redirect is what
// a merge whose follow failed leaves too; the failure is logged at Error.
func restoreRedirect(store any, id, winner string) {
	if winner == "" {
		return
	}
	ids := database.AsSyncIdentityStore(store)
	if ids == nil {
		return
	}
	if err := ids.RecordSyncMerge(id, winner); err != nil {
		mlog.Error("restore from trash: the sync redirect book=%s -> survivor=%s could not be put back: %s",
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
