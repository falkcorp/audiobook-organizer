// file: internal/merge/follow_journaled.go
// version: 1.3.1
// guid: 6a7e0c1a-cb17-41e5-bf0f-dd8903735f64
// last-edited: 2026-09-26

package merge

import (
	"errors"
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// ErrNoSyncFollower is returned by FollowAbsorbedJournaled when the store
// cannot carry sync identity or listening progress (it does not implement
// database.SyncIdentityStore). A caller whose absorbed book has progress must
// then refuse to retire that book, or the progress is lost at the purge.
var ErrNoSyncFollower = errors.New("merge: store cannot carry sync identity or listening progress")

// FollowAbsorbedJournaled carries one absorbed book's sync identity and every
// user's listening progress onto survivorID -- what CombineBooks does for each
// absorbed book -- and returns the journal entries UndoCombine needs to put it
// back: CombineAbsorbed.Progress and CombineAbsorbed.SyncRedirected.
//
// It exists for merge paths outside Service (dedup.MergeSplitBookCluster, and
// through it the chapter-group and split-book merges), which moved files and
// external IDs but never followed progress, so a reader's position on a
// soft-deleted chapter book was lost when the purge removed it.
func FollowAbsorbedJournaled(db UserProgressMerger, survivorID, absorbedID string, slice *SliceMapping, persist func([]CombineUserProgress) error) ([]CombineUserProgress, bool, error) {
	follower := database.AsSyncIdentityStore(db)
	if follower == nil {
		return nil, false, ErrNoSyncFollower
	}
	users, err := db.ListUsers()
	if err != nil {
		return nil, false, fmt.Errorf("list users: %w", err)
	}
	// Per user (snapshotPair): a user whose progress cannot be read is
	// skipped and logged, their rows left where they are, and only the users
	// in the before-snapshot are followed, so no progress moves unjournaled.
	before, followable, skipped := snapshotPair(db, users, absorbedID, survivorID)
	logSkippedFollow(absorbedID, "not journaled or followed; their rows stay on the absorbed book", skipped)
	// Persist the before-snapshot BEFORE the follow drains anything, so a
	// crash inside the follow still leaves undo what it needs to put the
	// absorbed book's progress back.
	if persist != nil {
		if err := persist(buildProgressJournal(before, progressSnap{})); err != nil {
			return nil, false, fmt.Errorf("journal progress before follow: %w", err)
		}
	}
	// followOneLoser writes the pending-repair record first; users skipped
	// above keep it alive so the sweep moves them once they read cleanly. An
	// error here means the move failed AND no record holds it: the caller
	// must not retire the absorbed book.
	if followable == nil {
		followable = []database.User{} // nil would mean every user
	}
	if err := followOneLoser(db, follower, survivorID, absorbedID, followable, slice, len(skipped) > 0); err != nil {
		return buildProgressJournal(before, progressSnap{}), true, err
	}
	after, _, afterSkipped := snapshotPair(db, followable, absorbedID, survivorID)
	// A user whose after-snapshot failed has no after-state in the journal;
	// undo then leaves their survivor progress as is (safe).
	logSkippedFollow(absorbedID, "followed, but the after-snapshot failed; undo leaves their survivor progress as is", afterSkipped)
	return buildProgressJournal(before, after), true, nil
}

// SliceMapping says where an absorbed book's audio starts in the survivor's
// timeline when the absorbed book is a SLICE of the survivor (a chapter or a
// split part), not another copy of the whole book.
type SliceMapping struct {
	OffsetSeconds float64
	// Mappable is false when the offset cannot be computed (a preceding
	// file's duration is unknown); no position is carried then.
	Mappable bool
}

// followSliceFor is mergeUserProgressFor for an absorbed book that is a SLICE of the
// survivor (a chapter / split part). Whole-book dedup merges compare
// ProgressPct across two different-length copies of one book and carry the
// further one's state, Finished and its iTunes play-count mark included. For a
// slice that is wrong: finishing chapter 2 is not finishing the book, and
// carrying that Finished would also suppress the next real iTunes play count.
// So here:
//
//   - the survivor's state (Finished included) is never replaced; a survivor
//     with no state gets an in-progress one, never Finished;
//   - no play-count mark is carried;
//   - the absorbed book's latest position is mapped into the survivor's
//     timeline (slice offset + position) and written only when it is further
//     than the survivor's own latest position, and only when Mappable;
//   - the rest of the whole-book rule still applies (user_state_merge.go):
//     last played (LastActivityAt) is the later of the two and
//     HideFromContinueListening is kept if either side has it. Bookmarks are
//     copied by moveLoserState like any merge.
//
// The absorbed side is drained as FollowMerge does. Nothing is lost: the
// caller journaled the absorbed book's state and positions beforehand, and
// UndoCombine writes them back.
func followSliceFor(db userPositionStore, userID, survivorID, absorbedID string, slice SliceMapping) error {
	loserState, err := db.GetUserBookState(userID, absorbedID)
	if err != nil {
		return fmt.Errorf("get absorbed state: %w", err)
	}
	loserPositions, err := db.ListUserPositionsForBook(userID, absorbedID)
	if err != nil {
		return fmt.Errorf("list absorbed positions: %w", err)
	}
	if !hasCarryableState(loserState, loserPositions) {
		return nil
	}
	var loserLatest *database.UserPosition
	for i := range loserPositions {
		if loserLatest == nil || loserPositions[i].UpdatedAt.After(loserLatest.UpdatedAt) {
			loserLatest = &loserPositions[i]
		}
	}
	survPositions, err := db.ListUserPositionsForBook(userID, survivorID)
	if err != nil {
		return fmt.Errorf("list survivor positions: %w", err)
	}
	var survLatest *database.UserPosition
	for i := range survPositions {
		if survLatest == nil || survPositions[i].UpdatedAt.After(survLatest.UpdatedAt) {
			survLatest = &survPositions[i]
		}
	}
	if slice.Mappable && loserLatest != nil {
		mapped := slice.OffsetSeconds + loserLatest.PositionSeconds
		if survLatest == nil || mapped > survLatest.PositionSeconds {
			seg := loserLatest.SegmentID
			if survLatest != nil {
				seg = survLatest.SegmentID
			}
			if err := carryPosition(db, userID, survivorID, database.UserPosition{SegmentID: seg, PositionSeconds: mapped, UpdatedAt: loserLatest.UpdatedAt}); err != nil {
				return fmt.Errorf("carry mapped position: %w", err)
			}
		}
	}
	survState, err := db.GetUserBookState(userID, survivorID)
	if err != nil {
		return fmt.Errorf("get survivor state: %w", err)
	}
	if survState != nil && loserState != nil {
		// Last played = max, hide = OR (the parts of the whole-book rule a
		// slice keeps). Status, FinishedAt and progress stay the survivor's.
		upd := *survState
		changed := false
		if loserState.LastActivityAt.After(upd.LastActivityAt) {
			upd.LastActivityAt = loserState.LastActivityAt
			changed = true
		}
		if loserState.HideFromContinueListening && !upd.HideFromContinueListening {
			upd.HideFromContinueListening = true
			changed = true
		}
		if changed {
			if err := db.SetUserBookState(&upd); err != nil {
				return fmt.Errorf("carry last-played/hide onto survivor: %w", err)
			}
		}
	}
	if survState == nil && loserState != nil && loserState.Status != "" {
		started := *loserState
		started.BookID = survivorID
		started.Status = database.UserBookStatusInProgress
		started.StatusManual = false
		started.FinishedAt = nil
		started.ProgressPct = 0
		started.TotalListenedSeconds = 0
		started.LastSegmentID = ""
		if err := db.SetUserBookState(&started); err != nil {
			return fmt.Errorf("start survivor state: %w", err)
		}
	}
	if len(loserPositions) > 0 {
		if err := db.ClearUserPositions(userID, absorbedID); err != nil {
			return fmt.Errorf("clear absorbed positions: %w", err)
		}
	}
	if loserState != nil {
		if err := db.SetUserBookState(drainedUserState(*loserState)); err != nil {
			return fmt.Errorf("drain absorbed state: %w", err)
		}
	}
	return nil
}

// BookHasUserProgress reports whether any user has a book state or a stored
// position on bookID.
func BookHasUserProgress(db UserProgressMerger, bookID string) (bool, error) {
	users, err := db.ListUsers()
	if err != nil {
		return false, fmt.Errorf("list users: %w", err)
	}
	states, positions, err := snapshotProgress(db, users, bookID)
	if err != nil {
		return false, err
	}
	return len(states) > 0 || len(positions) > 0, nil
}

// logSkippedFollow logs each user skipped by a journaled follow.
func logSkippedFollow(absorbedID, what string, skipped map[string]error) {
	for u, err := range skipped {
		mlog.Warn("merge-follow: progress of user=%s on absorbed=%s %s: %v",
			logger.SanitizeLogValue(u), logger.SanitizeLogValue(absorbedID), what, err)
	}
}
