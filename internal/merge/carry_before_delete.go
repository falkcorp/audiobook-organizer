// file: internal/merge/carry_before_delete.go
// version: 1.0.0
// guid: 3f1c9a52-7d4e-4b8a-a6c0-8e2b5d7f1a93
// last-edited: 2026-10-05

package merge

import (
	"errors"
	"fmt"
	"time"

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
// (a copy; doomedID keeps its own). If the reversal itself fails part way,
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
// pending user-state repair names it, and no user has carryable state on
// it.
func carryLeftover(db UserProgressMerger, doomedID string) error {
	recs, undecodable, err := ListPendingUserStateRepairs(db)
	if err != nil {
		return err
	}
	if len(undecodable) > 0 {
		return fmt.Errorf("%d undecodable pending user-state repair record(s); cannot tell whether one names %s", len(undecodable), doomedID)
	}
	for _, r := range recs {
		if r.LoserBookID == doomedID || r.WinnerBookID == doomedID {
			return fmt.Errorf("pending user-state repair %s -> %s is still open (state, bookmarks or the sync redirect did not all move)", r.LoserBookID, r.WinnerBookID)
		}
	}
	left, err := BookHasCarryableUserState(db, doomedID)
	if err != nil {
		return fmt.Errorf("verify the move: %w", err)
	}
	if left {
		return fmt.Errorf("listening state is still on %s after the move", doomedID)
	}
	return nil
}
