// file: internal/readstatus/readstatus.go
// version: 2.1.0
// guid: 6e2f8a1d-4c5b-4f70-a9c7-2d8e0f1b9a57
// last-edited: 2026-08-18
//
// RecomputeUserBookState derives a UserBookState from the current
// UserPosition rows for a given (user, book), honoring the
// status_manual override flag per spec 3.6 §6-7.
//
// Flow:
//   1. Load all book_files for the book (for segment durations)
//   2. Load all UserPosition rows for (user, book)
//   3. Sum listened-seconds: for each position, use min(position,
//      segment_duration). Total duration = sum of all segment
//      durations (including ones without positions).
//   4. Progress pct = 100 * listened / total (clamped 0..100).
//   5. If status_manual is true on the existing state, keep the
//      stored status and only refresh the activity fields.
//      Otherwise auto-derive:
//        - total_duration > 0 AND listened / total ≥ 0.95 → finished
//        - listened > 0 → in_progress
//        - else → unstarted
//      `abandoned` is never auto-computed — user-set only.

// Extracted from internal/server/ to internal/readstatus/ (pre-work P5)
// so internal/itunes/service/ can call Recompute / SetManual without
// creating a cyclic dep (server imports itunes/service; iTunes
// position-sync calls into here).

package readstatus

import (
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Store is what this package reads and writes to derive a read status.
//
// Measured with an empty-interface compiler probe: exactly these four, with no
// assignability constraints. Both functions below previously declared an
// ANONYMOUS composite of database.BookFileStore + database.UserPositionStore
// inline in their signatures -- roughly 86 methods for these four.
//
// An anonymous interface in a parameter position is the worst shape for this:
// it cannot be narrowed without editing every signature that repeats it, it
// cannot carry a //nolint, and interfacebloat never reports it because it is
// not a declaration. Naming it is what makes the width reviewable at all, and
// it is why callers had to satisfy the full surfaces to call a four-method
// function -- see internal/itunes/service/position_sync.go.
type Store interface {
	GetBookFiles(bookID string) ([]database.BookFile, error)
	ListUserPositionsForBook(userID, bookID string) ([]database.UserPosition, error)
	GetUserBookState(userID, bookID string) (*database.UserBookState, error)
	SetUserBookState(state *database.UserBookState) error
}

// RecomputeUserBookState reads positions + segment durations and
// updates user_book_state with fresh auto-computed fields. Returns
// the new state (or a no-op unchanged state if there's nothing to
// record — e.g. a user who's never touched this book).
func RecomputeUserBookState(store Store, userID, bookID string) (*database.UserBookState, error) {
	if store == nil || userID == "" || bookID == "" {
		return nil, nil
	}
	positions, err := store.ListUserPositionsForBook(userID, bookID)
	if err != nil {
		return nil, err
	}
	existing, _ := store.GetUserBookState(userID, bookID)

	// If no existing state and no positions, there's nothing to record.
	if len(positions) == 0 && existing == nil {
		return nil, nil
	}

	// Gather book_files for durations. Index by segment ID.
	files, _ := store.GetBookFiles(bookID)
	segDuration := make(map[string]float64, len(files))
	var totalDuration float64
	for _, f := range files {
		// BookFile.Duration is stored in seconds (per scanner convention).
		segDuration[f.ID] = float64(f.Duration)
		totalDuration += float64(f.Duration)
	}

	var listened float64
	var lastActivity time.Time
	var lastSegment string
	for _, pos := range positions {
		// Cap each contribution at the segment's duration — a client
		// could report a position past the end of a file.
		cap := segDuration[pos.SegmentID]
		p := pos.PositionSeconds
		if cap > 0 && p > cap {
			p = cap
		}
		listened += p
		if pos.UpdatedAt.After(lastActivity) {
			lastActivity = pos.UpdatedAt
			lastSegment = pos.SegmentID
		}
	}

	// Start from the existing state (preserves status_manual +
	// explicit status) or a fresh one.
	var state *database.UserBookState
	if existing != nil {
		copied := *existing
		state = &copied
	} else {
		state = &database.UserBookState{UserID: userID, BookID: bookID}
	}
	state.TotalListenedSeconds = listened
	if totalDuration > 0 {
		pct := int((listened / totalDuration) * 100)
		if pct < 0 {
			pct = 0
		}
		if pct > 100 {
			pct = 100
		}
		state.ProgressPct = pct
	} else {
		state.ProgressPct = 0
	}
	if !lastActivity.IsZero() {
		state.LastActivityAt = lastActivity
		state.LastSegmentID = lastSegment
	}

	// Auto-derive status unless user has manually overridden.
	if !state.StatusManual {
		switch {
		case totalDuration > 0 && listened/totalDuration >= database.FinishedThreshold:
			state.Status = database.UserBookStatusFinished
		case listened > 0:
			state.Status = database.UserBookStatusInProgress
		default:
			state.Status = database.UserBookStatusUnstarted
		}
	}

	if err := store.SetUserBookState(state); err != nil {
		return nil, err
	}
	return state, nil
}

// SetManualStatus records a user-forced status (Finished, Unstarted,
// Abandoned, or back to InProgress) and flips StatusManual=true so
// RecomputeUserBookState leaves it alone going forward. Passing
// empty string reverts to auto — next Recompute call derives a
// fresh status from positions.
func SetManualStatus(store Store, userID, bookID, status string) (*database.UserBookState, error) {
	if store == nil {
		return nil, nil
	}
	existing, _ := store.GetUserBookState(userID, bookID)
	var state *database.UserBookState
	if existing != nil {
		copied := *existing
		state = &copied
	} else {
		state = &database.UserBookState{UserID: userID, BookID: bookID}
	}
	if status == "" {
		state.StatusManual = false
		// Recompute to refresh status from positions.
		if err := store.SetUserBookState(state); err != nil {
			return nil, err
		}
		return RecomputeUserBookState(store, userID, bookID)
	}
	state.Status = status
	state.StatusManual = true
	state.LastActivityAt = time.Now()
	if err := store.SetUserBookState(state); err != nil {
		return nil, err
	}
	return state, nil
}
