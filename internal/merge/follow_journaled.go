// file: internal/merge/follow_journaled.go
// version: 1.1.0
// guid: 6a7e0c1a-cb17-41e5-bf0f-dd8903735f64
// last-edited: 2026-09-19

package merge

import (
	"errors"
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
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
	snap := func() (progressSnap, error) {
		var s progressSnap
		var err error
		if s.absState, s.absPos, err = snapshotProgress(db, users, absorbedID); err != nil {
			return s, err
		}
		s.survState, s.survPos, err = snapshotProgress(db, users, survivorID)
		return s, err
	}
	before, err := snap()
	if err != nil {
		return nil, false, fmt.Errorf("snapshot progress before follow: %w", err)
	}
	// Persist the before-snapshot BEFORE the follow drains anything, so a
	// crash inside the follow still leaves undo what it needs to put the
	// absorbed book's progress back.
	if persist != nil {
		if err := persist(buildProgressJournal(before, progressSnap{})); err != nil {
			return nil, false, fmt.Errorf("journal progress before follow: %w", err)
		}
	}
	if slice == nil {
		FollowMerge(db, follower, survivorID, []string{absorbedID})
	} else {
		if err := followSlice(db, follower, users, survivorID, absorbedID, *slice); err != nil {
			return buildProgressJournal(before, progressSnap{}), true, err
		}
	}
	after, err := snap()
	if err != nil {
		// The follow ran; without an after-snapshot undo cannot tell whether
		// the survivor changed since, so it would leave it as is (safe).
		return buildProgressJournal(before, progressSnap{}), true, fmt.Errorf("snapshot progress after follow: %w", err)
	}
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

// followSlice is FollowMerge for an absorbed book that is a SLICE of the
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
//     than the survivor's own latest position, and only when Mappable.
//
// The absorbed side is drained as FollowMerge does. Nothing is lost: the
// caller journaled the absorbed book's state and positions beforehand, and
// UndoCombine writes them back.
func followSlice(db UserProgressMerger, follower database.SyncIdentityStore, users []database.User, survivorID, absorbedID string, slice SliceMapping) error {
	if _, err := follower.MintOrGetSyncID(survivorID); err != nil {
		return fmt.Errorf("mint survivor sync id: %w", err)
	}
	if err := follower.RecordSyncMerge(absorbedID, survivorID); err != nil {
		return fmt.Errorf("record sync redirect: %w", err)
	}
	for _, u := range users {
		if u.ID == "" {
			continue
		}
		if err := followSliceFor(db, u.ID, survivorID, absorbedID, slice); err != nil {
			return fmt.Errorf("user %s: %w", u.ID, err)
		}
	}
	return nil
}

func followSliceFor(db userPositionStore, userID, survivorID, absorbedID string, slice SliceMapping) error {
	loserState, err := db.GetUserBookState(userID, absorbedID)
	if err != nil {
		return fmt.Errorf("get absorbed state: %w", err)
	}
	loserPositions, err := db.ListUserPositionsForBook(userID, absorbedID)
	if err != nil {
		return fmt.Errorf("list absorbed positions: %w", err)
	}
	if loserState == nil && len(loserPositions) == 0 {
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
			if err := db.SetUserPosition(userID, survivorID, seg, mapped); err != nil {
				return fmt.Errorf("carry mapped position: %w", err)
			}
		}
	}
	survState, err := db.GetUserBookState(userID, survivorID)
	if err != nil {
		return fmt.Errorf("get survivor state: %w", err)
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
		drained := *loserState
		drained.Status = ""
		drained.StatusManual = false
		drained.ProgressPct = 0
		drained.TotalListenedSeconds = 0
		drained.LastSegmentID = ""
		if err := db.SetUserBookState(&drained); err != nil {
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
