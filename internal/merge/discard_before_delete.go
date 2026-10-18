// file: internal/merge/discard_before_delete.go
// version: 1.2.0
// guid: fa2636c6-2ce1-42f5-a5bf-9952e4f6d7b4
// last-edited: 2026-10-18

package merge

import (
	"errors"
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// ErrDiscardIncomplete is returned by DiscardUserStateThenHardDelete when the
// book's listening state could not all be cleared, or a pending user-state
// repair still names the book. The delete did not run.
var ErrDiscardIncomplete = errors.New("merge: listening state was not fully discarded")

// DiscardedUserState counts what DiscardUserStateThenHardDelete cleared.
type DiscardedUserState struct {
	// Users is how many users had carryable state (hasCarryableState) or a
	// bookmark under the book's own sync id before it was cleared.
	Users int `json:"users"`
	// Bookmarks is how many bookmarks under the book's own sync id were
	// deleted.
	Bookmarks int `json:"bookmarks"`
}

// DiscardUserStateThenHardDelete clears every user's listening state on
// bookID -- their book state row, stored positions and the bookmarks under
// the book's own ABS sync id -- and then runs del (the caller's hard delete),
// all in ONE hold of the merge lock, so no merge, sweep or revert can put
// state back on bookID between the clear and the delete.
//
// It is the owner-triggered counterpart of CarryStateThenHardDelete: for a
// book in the trash with no live version-group sibling to carry the state
// to, the owner may choose to drop the state with the book. Nothing calls it
// automatically.
//
// precheck, when not nil, runs first under the same lock, before anything is
// cleared: the caller re-reads the book there (for example that it is still
// in the trash -- merge.RestoreFromTrash takes this lock too, so a restore
// cannot slip in between). Its error is returned as is, nothing cleared.
//
// It refuses (ErrDiscardIncomplete, nothing cleared, del not run) when a
// pending user-state repair names bookID: a move is still owed into or out
// of it, and discarding would race the repair. It lists users strictly
// (NewUserStateProbe), so an undecodable user row refuses too instead of
// leaving that user's state behind unseen. After clearing it re-checks with
// carryLeftover; anything left refuses and del does not run. A clear that
// fails part way leaves the book in place with some users cleared; running
// it again finishes the job.
//
// Bookmarks are cleared only under the book's own sync id. Bookmarks a merge
// copied here from an alias stay under that alias's id with the alias's row.
//
// The caller must hold neither the merge lock nor a version-group lock.
func DiscardUserStateThenHardDelete(db UserProgressMerger, bookID string, precheck, del func() error) (DiscardedUserState, error) {
	var res DiscardedUserState
	if bookID == "" {
		return res, fmt.Errorf("%w: empty book id", ErrDiscardIncomplete)
	}
	mergeSerializeMu.Lock()
	defer mergeSerializeMu.Unlock()

	if precheck != nil {
		if err := precheck(); err != nil {
			return res, err
		}
	}
	if err := pendingRepairNaming(db, bookID); err != nil {
		return res, fmt.Errorf("%w: %s: %w", ErrDiscardIncomplete, bookID, err)
	}
	probe, err := NewUserStateProbe(db)
	if err != nil {
		return res, fmt.Errorf("%w: %s: %w", ErrDiscardIncomplete, bookID, err)
	}
	deleter, canDelete := database.AsCapability[database.UserBookStateDeleter](db)
	counted := map[string]bool{}
	for _, u := range probe.users {
		if u.ID == "" {
			continue
		}
		had, err := discardUserState(db, deleter, canDelete, u.ID, bookID)
		if err != nil {
			return res, err
		}
		if had {
			counted[u.ID] = true
		}
	}

	n, err := discardOwnBookmarks(db, probe.users, bookID, counted)
	res.Bookmarks = n
	res.Users = len(counted)
	if err != nil {
		return res, fmt.Errorf("%w: %s: %w", ErrDiscardIncomplete, bookID, err)
	}

	if err := carryLeftover(db, bookID); err != nil {
		return res, fmt.Errorf("%w: %s after clearing: %w", ErrDiscardIncomplete, bookID, err)
	}
	if err := del(); err != nil {
		return res, fmt.Errorf("hard delete %s after its listening state was discarded: %w", bookID, err)
	}
	return res, nil
}

// discardUserState clears one user's state row and positions on bookID and
// reports whether they held carryable state. The read and the clear are one
// step under the (userID, bookID) user-state stripe
// (database.LockUserBookState), so a device sync lands either before the
// read (and is discarded with the rest, which is what the owner asked for)
// or after the clear, never between them where the clear would wipe it
// unseen. One that lands after the clear but before carryLeftover's re-check
// makes the delete refuse; one that lands after that re-check is a write to
// a book that is deleted next, like a write to any book being hard-deleted.
// Lock order: the caller holds the merge lock; this stripe is taken and
// released per user.
func discardUserState(db UserProgressMerger, deleter database.UserBookStateDeleter, canDelete bool, userID, bookID string) (had bool, err error) {
	defer database.LockUserBookState(userID, bookID)()
	st, err := db.GetUserBookState(userID, bookID)
	if err != nil {
		return false, fmt.Errorf("%w: read state user=%s book=%s: %w", ErrDiscardIncomplete, userID, bookID, err)
	}
	pos, err := db.ListUserPositionsForBook(userID, bookID)
	if err != nil {
		return false, fmt.Errorf("%w: read positions user=%s book=%s: %w", ErrDiscardIncomplete, userID, bookID, err)
	}
	had = hasCarryableState(st, pos)
	if len(pos) > 0 {
		if err := db.ClearUserPositions(userID, bookID); err != nil {
			return had, fmt.Errorf("%w: clear positions user=%s book=%s: %w", ErrDiscardIncomplete, userID, bookID, err)
		}
	}
	if st == nil {
		return had, nil
	}
	if canDelete {
		err = deleter.DeleteUserBookState(userID, bookID)
	} else {
		err = db.SetUserBookState(drainedUserState(*st))
	}
	if err != nil {
		return had, fmt.Errorf("%w: clear state user=%s book=%s: %w", ErrDiscardIncomplete, userID, bookID, err)
	}
	return had, nil
}

// discardOwnBookmarks deletes each user's bookmarks under bookID's own sync
// id, marking in users each user who had one. A store with no bookmark or
// sync-identity keyspace, or a book that was never given a sync id, has
// none.
func discardOwnBookmarks(db UserProgressMerger, users []database.User, bookID string, had map[string]bool) (int, error) {
	bs := database.AsBookmarkStore(db)
	ids := database.AsSyncIdentityStore(db)
	if bs == nil || ids == nil {
		return 0, nil
	}
	syncID, found, err := ids.GetSyncIDForBook(bookID)
	if err != nil {
		return 0, fmt.Errorf("read sync id: %w", err)
	}
	if !found || syncID == "" {
		return 0, nil
	}
	n := 0
	for _, u := range users {
		if u.ID == "" {
			continue
		}
		marks, err := bs.ListBookmarks(u.ID, syncID)
		if err != nil {
			return n, fmt.Errorf("list bookmarks user=%s: %w", u.ID, err)
		}
		if len(marks) > 0 {
			had[u.ID] = true
		}
		for _, m := range marks {
			if err := bs.DeleteBookmark(u.ID, syncID, m.TimeSec); err != nil {
				return n, fmt.Errorf("delete bookmark user=%s at %.3fs: %w", u.ID, m.TimeSec, err)
			}
			n++
		}
	}
	return n, nil
}
