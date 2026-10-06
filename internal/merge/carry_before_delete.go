// file: internal/merge/carry_before_delete.go
// version: 1.6.0
// guid: 3f1c9a52-7d4e-4b8a-a6c0-8e2b5d7f1a93
// last-edited: 2026-10-05

package merge

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// ErrStateCarryIncomplete is returned by CarryStateBeforeHardDelete when the
// book's listening state did not all reach the kept book. The caller must
// not hard-delete the book.
var ErrStateCarryIncomplete = errors.New("merge: listening state was not fully carried")

// CarryStateBeforeHardDelete moves everything a user has on doomedID onto
// keepID -- each user's book state and positions, their bookmarks, and the
// ABS sync identity (a redirect doomed -> keep) -- so the caller can then
// hard-delete doomedID without dropping it. It is for the automatic hard
// deletes that retire a live duplicate (reconcile.CleanupDuplicateVersionGroups,
// the iTunes regroup apply, the iTunes clone rollback).
//
// All or nothing. The move is FollowAbsorbedJournaled: a before-snapshot of
// both books per user, then the follow. It counts as complete only when the
// follow left no pending user-state repair for the pair -- followOneLoser
// deletes that record only once the redirect, every user's state AND the
// bookmark copy all succeeded, so a failed bookmark copy or redirect is
// caught here, not just a failed position write -- when no other pending
// repair names doomedID (a move still owed into or out of it), and when no
// user has carryable state left on doomedID (strict probe: an undecodable
// user row fails it).
//
// When it is not complete the follow is reversed (RestoreFollowedProgress:
// each user's state and positions back on doomedID, keepID's put back unless
// a user listened there since, the redirect cleared, the record dropped), so
// the users who did move are not left on keepID while doomedID -- the book
// the caller now keeps live -- holds the rest. The returned error wraps
// ErrStateCarryIncomplete either way. Bookmarks copied to keepID stay there
// (a copy; doomedID keeps its own). One path keeps more on keepID: when the
// follow itself returns an error (a move failed AND its repair record could
// not be written), it took no after-snapshot, so the reversal cannot tell
// the follow's writes on keepID from a user's own listening and leaves them
// (logged as warnings). doomedID still gets every user's state back; keepID
// may then hold a copy of what moved. Nothing is lost. If the reversal itself
// fails part way,
// a pending repair doomed -> keep is written again, so the owed move is held:
// the sweep defers it while doomedID is live, and the next carry attempt
// completes it forward (FollowAbsorbedJournaled runs completePendingInvolving
// first). Only when that record cannot be written either is the state split
// with nothing holding it; the error says so.
//
// A crash part way leaves the pending record the follow wrote before it
// moved anything; the next attempt completes it the same way.
//
// It takes LockMergeRMW for the carry, so the caller must hold neither it
// nor a version-group lock (lock order: merge lock, then group stripes).
func CarryStateBeforeHardDelete(db UserProgressMerger, keepID, doomedID string) error {
	if keepID == "" || doomedID == "" || keepID == doomedID {
		return fmt.Errorf("%w: invalid pair keep=%q doomed=%q", ErrStateCarryIncomplete, keepID, doomedID)
	}
	mergeSerializeMu.Lock()
	defer mergeSerializeMu.Unlock()
	return carryStateLocked(db, keepID, doomedID)
}

// ErrUserStateOnDoomedBook is returned by HardDeleteWithoutUserState when,
// right before the delete, the book holds listening state or a pending
// user-state repair names it. Nothing was deleted.
var ErrUserStateOnDoomedBook = errors.New("merge: listening state is on the book about to be hard-deleted")

// ErrUserStateCheckFailed is returned by HardDeleteWithoutUserState when the
// re-check right before the delete could not be made (a read failed, or a
// user row could not be decoded). Nothing was deleted.
var ErrUserStateCheckFailed = errors.New("merge: could not check the book about to be hard-deleted for listening state")

// errCarryCheckFailed marks a carryLeftover result that is a failed read,
// not state found or owed.
var errCarryCheckFailed = errors.New("check failed")

// ErrCarryPrecheckRefused is returned by CarryStateThenHardDelete when the
// caller's precheck, run under the merge lock before anything moved, refused
// the carry. It wraps the precheck's own error. Nothing was moved or deleted.
var ErrCarryPrecheckRefused = errors.New("merge: carry refused by its precheck")

// ErrSyncRedirectNotCleared is returned by CarryStateBetweenLiveBooks when the
// state moved but the ABS sync redirect between the two books could not be
// cleared. Unlike ErrStateCarryIncomplete, the state IS on the target book.
var ErrSyncRedirectNotCleared = errors.New("merge: listening state moved but the sync redirect was not cleared")

// CarryStateThenHardDelete is CarryStateBeforeHardDelete followed by del (the
// caller's hard delete of doomedID) in ONE hold of the merge lock, so no
// merge, sweep or revert can put state back on doomedID between the carry's
// final check and the delete. del runs only when the carry is complete; its
// error is returned wrapped. A carry that is not complete returns
// ErrStateCarryIncomplete (and was put back) with del not run.
//
// precheck, when non-nil, runs under that same hold before the carry: a
// condition the caller decided on before taking the lock (e.g. "keepID is a
// book ABS lists") is re-checked where no merge can change it before the
// carry starts. Its error refuses the carry with nothing moved, returned
// wrapped in ErrCarryPrecheckRefused (errors.Is reaches the caller's own
// error too).
//
// Client writes (ABS and web progress) do not take the merge lock, so this
// narrows that window to the carry's own last read, it cannot close it: a
// position a client writes to a book being deleted is lost with the book,
// the same as one written after the delete.
func CarryStateThenHardDelete(db UserProgressMerger, keepID, doomedID string, precheck, del func() error) error {
	if keepID == "" || doomedID == "" || keepID == doomedID {
		return fmt.Errorf("%w: invalid pair keep=%q doomed=%q", ErrStateCarryIncomplete, keepID, doomedID)
	}
	mergeSerializeMu.Lock()
	defer mergeSerializeMu.Unlock()
	if precheck != nil {
		if err := precheck(); err != nil {
			return fmt.Errorf("%w: %s -> %s: %w", ErrCarryPrecheckRefused, doomedID, keepID, err)
		}
	}
	if err := carryStateLocked(db, keepID, doomedID); err != nil {
		return err
	}
	if err := del(); err != nil {
		return fmt.Errorf("hard delete %s after its state moved to %s: %w", doomedID, keepID, err)
	}
	return nil
}

// HardDeleteWithoutUserState runs del (the caller's hard delete of bookID)
// under the merge lock after re-checking, under that same lock, that no user
// has carryable state on bookID and no pending user-state repair names it
// (the post-check CarryStateBeforeHardDelete uses: strict user listing, so an
// undecodable user row refuses). It is for a delete whose earlier probe found
// no state: anything that landed since refuses with ErrUserStateOnDoomedBook,
// a check that cannot be made with ErrUserStateCheckFailed, and nothing is
// deleted. The same limit as CarryStateThenHardDelete applies to client
// writes.
func HardDeleteWithoutUserState(db UserProgressMerger, bookID string, del func() error) error {
	if bookID == "" {
		return fmt.Errorf("%w: empty book id", ErrUserStateOnDoomedBook)
	}
	mergeSerializeMu.Lock()
	defer mergeSerializeMu.Unlock()
	if err := carryLeftover(db, bookID); err != nil {
		if errors.Is(err, errCarryCheckFailed) {
			return fmt.Errorf("%w: %s: %w", ErrUserStateCheckFailed, bookID, err)
		}
		return fmt.Errorf("%w: %s: %w", ErrUserStateOnDoomedBook, bookID, err)
	}
	return del()
}

// CarryStateBetweenLiveBooks moves every user's state, positions and
// bookmarks from fromID onto toID like CarryStateBeforeHardDelete (all or
// nothing, put back when incomplete), for two books that BOTH stay live: a
// rollback that failed part way and has to put its users' state back on the
// copy Audiobookshelf lists. The hard-delete carry records the ABS sync
// redirect fromID -> toID because fromID is about to be deleted; here fromID
// stays, so once the state has moved the redirect is cleared again, in both
// directions (an earlier carry the other way recorded toID -> fromID), and
// each book's sync id resolves to itself. A redirect that cannot be cleared
// is an error wrapping ErrSyncRedirectNotCleared: the state has moved, but an
// ABS client holding one book's id would still land on the other.
func CarryStateBetweenLiveBooks(db UserProgressMerger, toID, fromID string) error {
	if toID == "" || fromID == "" || toID == fromID {
		return fmt.Errorf("%w: invalid pair to=%q from=%q", ErrStateCarryIncomplete, toID, fromID)
	}
	mergeSerializeMu.Lock()
	defer mergeSerializeMu.Unlock()
	if err := carryStateLocked(db, toID, fromID); err != nil {
		return err
	}
	clearer, ok := database.AsCapability[syncMergeClearer](db)
	if !ok {
		return fmt.Errorf("%w: state moved %s -> %s, but the store cannot clear the sync redirect between them", ErrSyncRedirectNotCleared, fromID, toID)
	}
	for _, pair := range [][2]string{{fromID, toID}, {toID, fromID}} {
		if err := clearer.ClearSyncMerge(pair[0], pair[1]); err != nil {
			return fmt.Errorf("%w: state moved %s -> %s, but clearing the sync redirect %s -> %s failed: %w", ErrSyncRedirectNotCleared, fromID, toID, pair[0], pair[1], err)
		}
	}
	return nil
}

// carryStateLocked is CarryStateBeforeHardDelete's body. The caller holds
// mergeSerializeMu and has validated the pair.
func carryStateLocked(db UserProgressMerger, keepID, doomedID string) error {
	progress, redirected, err := FollowAbsorbedJournaled(db, keepID, doomedID, nil, nil)
	if errors.Is(err, ErrNoSyncFollower) {
		// Nothing moved: without a sync layer the redirect and bookmarks
		// cannot follow, so the carry is refused rather than done in part.
		return fmt.Errorf("%w: %w", ErrStateCarryIncomplete, err)
	}
	cause := err
	if cause == nil {
		cause = carryLeftover(db, doomedID)
	}
	if cause == nil {
		return nil
	}
	warnings, rerr := RestoreFollowedProgress(db, keepID, doomedID, redirected, progress)
	for _, w := range warnings {
		mlog.Warn("carry before delete: %s", logger.SanitizeLogValue(w))
	}
	if rerr == nil {
		// A one-shot put-back: no journal re-runs it, so its reconcile
		// markers go.
		dropSurvivorReconcileMarkers(db, keepID, doomedID, progress)
		return fmt.Errorf("%w: %s -> %s: %w; every user's state was put back on %s", ErrStateCarryIncomplete, doomedID, keepID, cause, doomedID)
	}
	if perr := putPendingRepair(db, PendingUserStateRepair{LoserBookID: doomedID, WinnerBookID: keepID, RecordedAt: time.Now().UTC()}); perr != nil {
		return fmt.Errorf("%w: %s -> %s: %w; putting it back failed (%w) and no repair record holds the move (%w)",
			ErrStateCarryIncomplete, doomedID, keepID, cause, rerr, perr)
	}
	return fmt.Errorf("%w: %s -> %s: %w; putting it back failed (%w); a pending user-state repair holds the move",
		ErrStateCarryIncomplete, doomedID, keepID, cause, rerr)
}

// carryLeftover is nil when nothing of doomedID's is still owed or left: no
// pending user-state repair names it (pendingRepairNaming), and no user has
// carryable state on it. A read that fails is wrapped with
// errCarryCheckFailed.
func carryLeftover(db UserProgressMerger, doomedID string) error {
	if err := pendingRepairNaming(db, doomedID); err != nil {
		return err
	}
	left, err := BookHasCarryableUserState(db, doomedID)
	if err != nil {
		return fmt.Errorf("verify the move: %w: %w", errCarryCheckFailed, err)
	}
	if left {
		return fmt.Errorf("listening state is still on %s after the move", doomedID)
	}
	return nil
}

// pendingRepairNaming is nil when no pending user-state repair, decodable or
// not, names bookID: no move of state, bookmarks or the sync redirect is
// still owed into or out of it. It is the one check both the carry
// (carryLeftover, after the move) and the discard (before it clears
// anything) make. A failed listing is wrapped with errCarryCheckFailed, so
// a caller can tell "could not check" from "a repair is open".
func pendingRepairNaming(db UserProgressMerger, bookID string) error {
	recs, undecodable, err := ListPendingUserStateRepairs(db)
	if err != nil {
		return fmt.Errorf("list pending user-state repairs: %w: %w", errCarryCheckFailed, err)
	}
	// An undecodable record still has its key, which names its pair
	// (pendingRepairKey): only one naming bookID counts.
	for _, k := range undecodable {
		pair := strings.Split(strings.TrimPrefix(k, PendingUserStateRepairPrefix), ":")
		if slices.Contains(pair, bookID) {
			return fmt.Errorf("undecodable pending user-state repair %s names %s", k, bookID)
		}
	}
	for _, r := range recs {
		if r.LoserBookID == bookID || r.WinnerBookID == bookID {
			return fmt.Errorf("pending user-state repair %s -> %s is still open (state, bookmarks or the sync redirect did not all move); let the repair sweep finish it first", r.LoserBookID, r.WinnerBookID)
		}
	}
	return nil
}
