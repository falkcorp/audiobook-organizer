// file: internal/merge/follow_journaled.go
// version: 1.0.0
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
func FollowAbsorbedJournaled(db UserProgressMerger, survivorID, absorbedID string) ([]CombineUserProgress, bool, error) {
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
	FollowMerge(db, follower, survivorID, []string{absorbedID})
	after, err := snap()
	if err != nil {
		// The follow ran; without an after-snapshot undo cannot tell whether
		// the survivor changed since, so it would leave it as is (safe).
		return buildProgressJournal(before, progressSnap{}), true, fmt.Errorf("snapshot progress after follow: %w", err)
	}
	return buildProgressJournal(before, after), true, nil
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
