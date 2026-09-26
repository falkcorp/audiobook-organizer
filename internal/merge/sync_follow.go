// file: internal/merge/sync_follow.go
// version: 1.7.1
// guid: 50421381-9def-4b19-bd23-6fa1a03c24d3
// last-edited: 2026-09-26

// Package merge: sync-identity follow hooks.
//
// The ABS-compatible sync layer exposes a durable `libraryItemId` (a syncID,
// see internal/database/pebble_store_syncid.go) rather than the raw Book ULID,
// because this app's core loop -- moving, retagging and merging books --
// churns ULIDs. Every code path that retires or replaces a Book ULID must
// therefore carry the syncID (and the per-user listening position keyed to the
// old ULID) forward, or a device's place in a book is silently orphaned. On the
// HARD-delete path (dedup.MergeBooks) there is no surviving row to repoint
// afterwards, so an un-followed merge there is unrecoverable. CombineBooks
// soft-deletes and journals (since 2026-09-13); UndoCombine reverses its
// follows with ClearSyncMerge and FollowFileMove in the other direction.
//
// See docs/specs/2026-07-29-abs-sync-api-design.md §4.2 (model), §4.3 (test
// bar) and §5.5 (progress on merge).
//
// A second, file-scoped identity layer sits alongside the book-level one:
// each BookFile has a durable sync_file `ino` backing the ABS contentUrl
// scheme `/api/items/{itemId}/file/{ino}` (internal/database's
// pebble_store_syncfile.go). PR #2074 wired item-level identity and progress
// through CombineBooks and the untagged-move path but left per-file `ino`
// behind, since sync_file entries are keyed (bookID, fileID) and the old
// RepointSyncFile could only move a fileID within one book, never across
// books. FollowFileMove (called from CombineBooks) and the file-carry inside
// FollowBookIDChange (covering the untagged-move path) close that gap using
// the new database.RepointSyncFileToBook primitive.
package merge

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// SyncFollower is satisfied by anything that can record a merge in the
// sync-identity layer (internal/database's SyncIdentityStore). Optional: nil
// is a valid, no-op value -- a store that does not implement it (a mock, a
// MemStore) simply means merges do not touch sync identity.
type SyncFollower interface {
	MintOrGetSyncID(bookID string) (string, error)
	RecordSyncMerge(loserBookID, winnerBookID string) error
}

// syncRepointMu serializes FollowBookIDChange's read-then-repoint. Unlike the
// merge paths (which run inside mergeSerializeMu already), the scanner's
// version-link path is NOT under any merge lock and runs on parallel scan
// workers, and RepointSyncItem's own GetSyncIDForBook -> batch-commit is not
// atomic. Without this, two workers version-linking against the SAME
// predecessor book could both read its syncID and both write a reverse index,
// leaving two live books pointing at one syncID. Deliberately NOT
// mergeSerializeMu: that lock is held across whole merges and a scan worker
// blocking on it (or vice versa) is a needless coupling of two unrelated
// subsystems.
var syncRepointMu sync.Mutex

// FollowMerge carries sync identity and every user's state (upos, ubs,
// bookmarks) from every loser onto the winner of a merge. Call it while still
// holding whatever lock guards the merge's read-modify-write, BEFORE any hard
// delete of a loser row.
//
// Never silently lossy (owner requirement 2026-09-26). For each loser a
// durable pending-repair record is written BEFORE anything moves and deleted
// only after everything moved (pending_repair.go). A failure to move leaves
// the record for maintenance.repair-merged-user-state to complete, and the
// merge may proceed. The returned error is non-nil only when a move failed
// AND its record could not be written: nothing then holds the owed move, and
// the caller must not retire the loser (or, when it already has, must report
// the merge as failed).
//
// User state no longer depends on sync identity: a store without the
// capability, or a failure to mint the winner's syncID, used to skip every
// user's progress along with the redirect.
//
// Exactly-once: the store primitives it calls are idempotent
// (MintOrGetSyncID returns the existing id; RecordSyncMerge returns early when
// the redirect is already recorded; a drained loser row is not carryable,
// hasCarryableState), so a retried or replayed merge moves nothing twice.
func FollowMerge(db UserProgressMerger, follower SyncFollower, winnerBookID string, loserBookIDs []string) error {
	return followMerge(db, follower, winnerBookID, loserBookIDs, nil, false)
}

// FollowMergeUsers is FollowMerge restricted to the given users' state.
// Journaled callers pass only the users they could snapshot, so state is never
// moved without the journal undo needs to move it back. incomplete says some
// users were left out (their rows could not be read): the pending-repair
// record is then KEPT, so the sweep moves them once they read cleanly. A nil
// or empty list follows NO user's state (the redirect is still recorded).
func FollowMergeUsers(db UserProgressMerger, follower SyncFollower, winnerBookID string, loserBookIDs []string, users []database.User, incomplete bool) error {
	if users == nil {
		users = []database.User{}
	}
	return followMerge(db, follower, winnerBookID, loserBookIDs, users, incomplete)
}

// followMerge: users == nil means "every user" (ListUsers).
func followMerge(db UserProgressMerger, follower SyncFollower, winnerBookID string, loserBookIDs []string, users []database.User, incomplete bool) error {
	if db == nil || winnerBookID == "" {
		return nil
	}
	var errs []error
	for _, loserID := range loserBookIDs {
		if loserID == "" || loserID == winnerBookID {
			continue
		}
		if err := followOneLoser(db, follower, winnerBookID, loserID, users, nil, incomplete); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// errUsersSkipped is the cause recorded when a journaled follow left out
// users whose rows could not be read.
var errUsersSkipped = errors.New("some users' state could not be read and was not moved")

// followOneLoser is the record -> move -> clear sequence for one loser. See
// FollowMerge for the error contract.
func followOneLoser(db UserProgressMerger, follower SyncFollower, winnerBookID, loserBookID string, users []database.User, slice *SliceMapping, incomplete bool) error {
	recErr := putPendingRepair(db, PendingUserStateRepair{
		LoserBookID: loserBookID, WinnerBookID: winnerBookID, RecordedAt: time.Now().UTC(), Slice: slice,
	})
	moveErr := moveLoserState(db, follower, winnerBookID, loserBookID, users, slice)
	if moveErr == nil && !incomplete {
		if err := db.DeleteRaw(pendingRepairKey(loserBookID, winnerBookID)); err != nil {
			// Harmless: the sweep re-runs an idempotent follow and deletes it.
			mlog.Warn("merge-follow: state moved but pending record for loser=%s not deleted: %s",
				logger.SanitizeLogValue(loserBookID), logger.SanitizeLogValue(fmt.Sprint(err)))
		}
		return nil
	}
	cause := moveErr
	if cause == nil {
		cause = errUsersSkipped
	}
	if recErr == nil {
		logPendingLeft(loserBookID, winnerBookID, cause)
		return nil
	}
	mlog.Error("merge-follow: user state of loser=%s NOT moved to winner=%s and NO pending-repair record could be written: move: %s; record: %s",
		logger.SanitizeLogValue(loserBookID), logger.SanitizeLogValue(winnerBookID),
		logger.SanitizeLogValue(fmt.Sprint(cause)), logger.SanitizeLogValue(fmt.Sprint(recErr)))
	return fmt.Errorf("user state of %s not moved to %s (%w) and no repair record written: %w", loserBookID, winnerBookID, cause, recErr)
}

// moveLoserState does the moves: identity redirect, each user's upos/ubs
// under the conflict rule (user_state_merge.go) or the slice rule, then
// bookmarks. Every step runs even when an earlier one failed; the joined
// error names each failure. users == nil means every user.
func moveLoserState(db UserProgressMerger, follower SyncFollower, winnerBookID, loserBookID string, users []database.User, slice *SliceMapping) error {
	var errs []error
	if follower != nil {
		if _, err := follower.MintOrGetSyncID(winnerBookID); err != nil {
			errs = append(errs, fmt.Errorf("mint winner syncID: %w", err))
		} else if err := follower.RecordSyncMerge(loserBookID, winnerBookID); err != nil {
			errs = append(errs, fmt.Errorf("record sync redirect: %w", err))
		}
	}
	all, listErr := db.ListUsers()
	if listErr != nil {
		errs = append(errs, fmt.Errorf("list users: %w", listErr))
	}
	if users == nil {
		users = all
	}
	for _, u := range users {
		if u.ID == "" {
			continue
		}
		var err error
		if slice != nil {
			err = followSliceFor(db, u.ID, winnerBookID, loserBookID, *slice)
		} else {
			err = mergeUserProgressFor(db, u.ID, loserBookID, winnerBookID)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("user %s: %w", u.ID, err))
		}
	}
	// Bookmarks for every user, not just the followed ones: a copy is
	// non-destructive and needs no journal.
	if follower != nil && listErr == nil {
		if err := copyBookmarksForMerge(db, all, loserBookID, winnerBookID); err != nil {
			errs = append(errs, fmt.Errorf("bookmarks: %w", err))
		}
	}
	return errors.Join(errs...)
}

// FollowMergeWithStore is FollowMerge for callers that hold only a store and
// cannot reach a *Service (dedup.MergeBooks). It derives the follower by
// capability; a store without sync identity still has every user's upos/ubs
// moved (only the redirect and bookmarks need identity).
func FollowMergeWithStore(db UserProgressMerger, winnerBookID string, loserBookIDs []string) error {
	return FollowMerge(db, asFollower(db), winnerBookID, loserBookIDs)
}

// FollowFileMove carries each moved file's sync_file identity (its durable
// `ino`) from oldBookID to newBookID after the caller has physically moved
// the underlying BookFile row(s) onto a different book -- CombineBooks
// (MoveBookFilesToBook, and attachVirtualFile's cross-book reattach branch).
// Call it with the EXACT list of file IDs that were just moved, still inside
// whatever lock guards the operation's read-modify-write, and BEFORE the
// source book row is retired. UndoCombine calls it in the reverse direction
// (survivor -> restored book) to carry the inos back.
//
// Best-effort and per-file: one file's repoint failing does not stop the
// others from being attempted. A store that does not implement
// SyncFileStore is a silent no-op everywhere else sync_file lives, but a
// skipped follow here strands every moved file's ino on a book that no longer
// owns the file -- logged at Warn once for the whole call, matching
// FollowMergeWithStore's severity. Each individual
// repoint failure is logged at ERROR with both book IDs and the file ID,
// because a silent failure here quietly breaks a user's downloaded file with
// no trace.
//
// Idempotent and no-op-safe by construction: RepointSyncFileToBook is
// idempotent (a re-run finds the source entry already moved) and empty
// fileIDs entries are skipped.
func FollowFileMove(db syncCapabilityStore, oldBookID, newBookID string, fileIDs []string) {
	if db == nil || oldBookID == "" || newBookID == "" || oldBookID == newBookID || len(fileIDs) == 0 {
		return
	}
	sf := database.AsSyncFileStore(db)
	if sf == nil {
		slog.Warn("sync-identity file-follow: store does not implement SyncFileStore; file ino(s) will NOT be carried forward",
			"old_book", oldBookID, "new_book", newBookID, "file_count", len(fileIDs))
		return
	}
	for _, fileID := range fileIDs {
		if fileID == "" {
			continue
		}
		if err := sf.RepointSyncFileToBook(oldBookID, newBookID, fileID); err != nil {
			slog.Error("sync-identity file-follow: repoint FAILED; a cached download URL for this file will go stale",
				"old_book", oldBookID, "new_book", newBookID, "file_id", fileID, "err", err)
		}
	}
}

// followSyncFilesForBookChange is FollowFileMove's counterpart for
// FollowBookIDChange's untagged-move path: unlike CombineBooks, the caller
// there (internal/scanner) does not carry an explicit list of moved file
// IDs, so this enumerates every sync_file already registered on oldBookID
// via ListSyncFilesForBook and repoints each by its CurrentFileID. A book's
// BookFile IDs are stable across the version-link (only the Book ULID
// changes), so the enumerated fileIDs are exactly the ones that need to
// move.
//
// Debug, not warn, when the store lacks the capability: both rows survive a
// version-link (this is not a hard-delete path), matching
// FollowBookIDChange's own severity choice for the identity/progress carry.
func followSyncFilesForBookChange(db syncCapabilityStore, oldBookID, newBookID string) {
	sf := database.AsSyncFileStore(db)
	if sf == nil {
		slog.Debug("sync-identity follow: store does not implement SyncFileStore; skipping file ino carry-forward",
			"old_book", oldBookID, "new_book", newBookID)
		return
	}
	files, err := sf.ListSyncFilesForBook(oldBookID)
	if err != nil {
		slog.Error("sync-identity follow: could not list old book's sync_files; file ino(s) NOT carried forward",
			"old_book", oldBookID, "new_book", newBookID, "err", err)
		return
	}
	for _, f := range files {
		if err := sf.RepointSyncFileToBook(oldBookID, newBookID, f.CurrentFileID); err != nil {
			slog.Error("sync-identity follow: file repoint FAILED; a cached download URL for this file will go stale",
				"old_book", oldBookID, "new_book", newBookID, "file_id", f.CurrentFileID, "sync_file_id", f.SyncFileID, "err", err)
		}
	}
}

// FollowBookIDChange carries a book's sync identity and per-user progress from
// oldBookID to newBookID when a Book ULID is REPLACED rather than merged --
// the untagged-move case, where a moved file is re-scanned, matched to its
// predecessor by hash, and a brand-new ULID is minted for the new path
// (internal/scanner's version-link path). Both rows survive there, so this is
// a repoint (RepointSyncItem), not a redirect.
//
// No-op when oldBookID has no syncID yet: there is no client-visible identity
// to carry, and in that case the per-user progress rows are deliberately left
// where they are -- moving them would change long-standing scanner behaviour
// for installs that do not use the sync layer at all.
//
// Idempotent: a second call finds no syncID on oldBookID (the reverse index
// moved) and returns without touching anything.
func FollowBookIDChange(db UserProgressMerger, oldBookID, newBookID string) {
	if db == nil || oldBookID == "" || newBookID == "" || oldBookID == newBookID {
		return
	}
	ids := database.AsSyncIdentityStore(db)
	if ids == nil {
		// Debug, not warn (unlike FollowMergeWithStore): both rows survive a
		// version-link, so a skipped follow here is recoverable by repointing
		// later, and this runs on every hash-duplicate import.
		slog.Debug("sync-identity follow: store does not implement SyncIdentityStore; skipping repoint",
			"old_book", oldBookID, "new_book", newBookID)
		return
	}

	syncRepointMu.Lock()
	defer syncRepointMu.Unlock()

	syncID, has, err := ids.GetSyncIDForBook(oldBookID)
	if err != nil {
		slog.Error("sync-identity follow: could not read the old book's syncID; identity NOT carried forward",
			"old_book", oldBookID, "new_book", newBookID, "err", err)
		return
	}
	if !has {
		return
	}

	if err := ids.RepointSyncItem(oldBookID, newBookID); err != nil {
		slog.Error("sync-identity follow: repoint FAILED; clients still resolve to the retired book id",
			"sync_id", syncID, "old_book", oldBookID, "new_book", newBookID, "err", err)
		return
	}

	// Carry each registered file's ino forward too. Independent of progress
	// below (a file-repoint failure must not block a progress carry or vice
	// versa), so this runs unconditionally once identity itself is repointed
	// rather than being gated behind the progress-merge outcome.
	followSyncFilesForBookChange(db, oldBookID, newBookID)

	// The identity now resolves to newBookID, so progress keyed to oldBookID
	// would look lost to a client. It is still on disk under the old id (this
	// only fails forward, never destroys), but it must be logged loudly.
	if err := mergeUserProgress(db, oldBookID, newBookID); err != nil {
		slog.Error("sync-identity follow: identity repointed but progress NOT migrated; positions still keyed to the old book id",
			"sync_id", syncID, "old_book", oldBookID, "new_book", newBookID, "err", err)
		return
	}
	slog.Info("sync-identity followed a book id change",
		"sync_id", syncID, "old_book", oldBookID, "new_book", newBookID)
}

// carryPlayCountMark moves the loser book's ITunesPlayCountBumpedAt onto the
// winner when it is later than the winner's, so a carried finish the loser's
// iTunes track already counted is not counted again (see mergeUserProgressFor).
// A loser with no mark, or no longer present, carries nothing.
func carryPlayCountMark(db playCountMarkStore, loserBookID, winnerBookID string) error {
	loser, err := db.GetBookByID(loserBookID)
	if err != nil {
		return fmt.Errorf("read loser %s: %w", loserBookID, err)
	}
	if loser == nil || loser.ITunesPlayCountBumpedAt == nil {
		return nil
	}
	mark := *loser.ITunesPlayCountBumpedAt
	_, err = db.ModifyBook(winnerBookID, func(b *database.Book) error {
		if b.ITunesPlayCountBumpedAt != nil && !mark.After(*b.ITunesPlayCountBumpedAt) {
			return database.ErrSkipBookWrite
		}
		b.ITunesPlayCountBumpedAt = &mark
		return nil
	})
	return err
}

// mergeUserProgress merges every user's listening progress on loserBookID onto
// winnerBookID, then drains the loser side.
//
// The loop is over USERS, not books: UserBookState/UserPosition are keyed
// user-first (`ubs:<userID>:<bookID>`, `upos:<userID>:<bookID>:<segmentID>`)
// with no book -> users reverse index, so there is no way to ask "who has
// progress on this book". This is bounded by the number of accounts on the
// instance (a household, not the library), so a plain sequential loop is
// correct here -- deliberately NOT a worker pool, per CLAUDE.md's own
// "whole-library-scale" threshold.
func mergeUserProgress(db UserProgressMerger, loserBookID, winnerBookID string) error {
	users, err := db.ListUsers()
	if err != nil {
		return fmt.Errorf("list users: %w", err)
	}
	return mergeUserProgressUsers(db, users, loserBookID, winnerBookID)
}

// mergeUserProgressUsers merges the given users' progress, one at a time.
func mergeUserProgressUsers(db UserProgressMerger, users []database.User, loserBookID, winnerBookID string) error {
	var firstErr error
	for _, u := range users {
		if u.ID == "" {
			continue
		}
		if err := mergeUserProgressFor(db, u.ID, loserBookID, winnerBookID); err != nil {
			// Keep going: one user's failure must not strand every other
			// user's position.
			slog.Error("sync-identity merge-follow: progress merge failed for one user",
				"user", u.ID, "loser", loserBookID, "winner", winnerBookID, "err", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// mergeUserProgressFor merges one user's state from loser onto winner under
// the conflict rule documented in user_state_merge.go (finished sticky,
// newest lastUpdate wins the position, last played = max, hide = OR).
//
// The loser side is drained ONLY after a successful write, so a mid-way store
// failure can never destroy the state it was supposed to carry forward. A
// loser with no carryable state (never had any, or drained by an earlier
// follow) is a no-op, which is what makes a replayed follow safe.
//
// When the loser was Finished, its book's ITunesPlayCountBumpedAt is carried
// onto the winner when later than the winner's own. The rule: one listen is
// one iTunes play-count bump across a merge, not one per iTunes track that
// ever held it.
func mergeUserProgressFor(db userPositionStore, userID, loserBookID, winnerBookID string) error {
	loserState, err := db.GetUserBookState(userID, loserBookID)
	if err != nil {
		return fmt.Errorf("get loser state: %w", err)
	}
	loserPositions, err := db.ListUserPositionsForBook(userID, loserBookID)
	if err != nil {
		return fmt.Errorf("list loser positions: %w", err)
	}
	if !hasCarryableState(loserState, loserPositions) {
		return nil // nothing to merge for this user
	}

	winnerState, err := db.GetUserBookState(userID, winnerBookID)
	if err != nil {
		return fmt.Errorf("get winner state: %w", err)
	}
	winnerPositions, err := db.ListUserPositionsForBook(userID, winnerBookID)
	if err != nil {
		return fmt.Errorf("list winner positions: %w", err)
	}

	plan := planUserStateMerge(userID, winnerBookID,
		userStateSide{state: loserState, positions: loserPositions},
		userStateSide{state: winnerState, positions: winnerPositions})

	if plan.state != nil {
		if err := db.SetUserBookState(plan.state); err != nil {
			return fmt.Errorf("write merged state onto winner: %w", err)
		}
	}
	if plan.loserFinished {
		if err := carryPlayCountMark(db, loserBookID, winnerBookID); err != nil {
			return fmt.Errorf("carry play-count mark: %w", err)
		}
	}
	if plan.loserPositionsWin {
		// Segment IDs are opaque per-user bookkeeping, carried as-is, oldest
		// first so the loser's latest stays the winner's latest.
		for _, pos := range sortPositionsOldestFirst(loserPositions) {
			if err := carryPosition(db, userID, winnerBookID, pos); err != nil {
				return fmt.Errorf("carry position %s onto winner: %w", pos.SegmentID, err)
			}
		}
	}

	// Drain the loser whichever side won: its book is merged away, and state
	// only resolvable under a retired id is a leak.
	if len(loserPositions) > 0 {
		if err := db.ClearUserPositions(userID, loserBookID); err != nil {
			return fmt.Errorf("clear loser positions: %w", err)
		}
	}
	if loserState != nil {
		if err := db.SetUserBookState(drainedUserState(*loserState)); err != nil {
			return fmt.Errorf("drain loser state: %w", err)
		}
	}
	return nil
}
