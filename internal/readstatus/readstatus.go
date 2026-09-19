// file: internal/readstatus/readstatus.go
// version: 2.3.0
// guid: 6e2f8a1d-4c5b-4f70-a9c7-2d8e0f1b9a57
// last-edited: 2026-09-19
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
	"errors"
	"fmt"
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

// ErrStateUnreadable is returned when the stored user_book_state row could not
// be read (I/O or decode error). That is NOT "no row yet": writing a fresh row
// over it would silently drop the fields only the stored row carries (manual
// status, hide-from-continue-listening, the progress-reset tombstone), so
// nothing is written. Handlers map it to 503.
var ErrStateUnreadable = errors.New("readstatus: stored book state is unreadable")

// ErrPositionsUnreadable is returned when the user's stored positions could
// not be read. Not "no positions yet": deriving a status from an empty list
// would reset a listened book to unstarted. Nothing is written; handlers map
// it to 503.
var ErrPositionsUnreadable = errors.New("readstatus: stored positions are unreadable")

// ErrBookFilesUnreadable is returned when the book's files (the durations
// the status is derived from) could not be read. Nothing is written; 503.
var ErrBookFilesUnreadable = errors.New("readstatus: book files are unreadable")

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
		return nil, fmt.Errorf("%w: %s/%s: %w", ErrPositionsUnreadable, userID, bookID, err)
	}
	existing, err := store.GetUserBookState(userID, bookID)
	if err != nil {
		return nil, fmt.Errorf("%w: %s/%s: %w", ErrStateUnreadable, userID, bookID, err)
	}

	// If no existing state and no positions, there's nothing to record.
	if len(positions) == 0 && existing == nil {
		return nil, nil
	}
	state, err := deriveState(store, userID, bookID, existing, positions)
	if err != nil {
		return nil, err
	}
	if err := store.SetUserBookState(state); err != nil {
		return nil, err
	}
	return state, nil
}

// deriveState computes the auto fields of a UserBookState IN MEMORY from the
// positions and the book's file durations, starting from a copy of existing
// (which keeps status_manual, an explicit status and the fields this package
// does not own) or a fresh row. It writes nothing. The files read fails
// closed: without durations the derived status and percent are wrong.
func deriveState(store Store, userID, bookID string, existing *database.UserBookState, positions []database.UserPosition) (*database.UserBookState, error) {
	files, err := store.GetBookFiles(bookID)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrBookFilesUnreadable, bookID, err)
	}
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

	var state *database.UserBookState
	if existing != nil {
		copied := *existing
		state = &copied
	} else {
		state = &database.UserBookState{UserID: userID, BookID: bookID}
	}
	state.TotalListenedSeconds = listened
	if totalDuration > 0 {
		state.ProgressPct = min(max(int((listened/totalDuration)*100), 0), 100)
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
	existing, err := store.GetUserBookState(userID, bookID)
	if err != nil {
		return nil, fmt.Errorf("%w: %s/%s: %w", ErrStateUnreadable, userID, bookID, err)
	}
	var state *database.UserBookState
	if existing != nil {
		copied := *existing
		state = &copied
	} else {
		state = &database.UserBookState{UserID: userID, BookID: bookID}
	}
	if status == "" {
		// Back to auto: derive the new state IN MEMORY with the flag cleared
		// and write it ONCE. Every read happens before the write, so a failed
		// read leaves the stored row (flag included) untouched.
		positions, err := store.ListUserPositionsForBook(userID, bookID)
		if err != nil {
			return nil, fmt.Errorf("%w: %s/%s: %w", ErrPositionsUnreadable, userID, bookID, err)
		}
		state.StatusManual = false
		derived, err := deriveState(store, userID, bookID, state, positions)
		if err != nil {
			return nil, err
		}
		if err := store.SetUserBookState(derived); err != nil {
			return nil, err
		}
		return derived, nil
	}
	state.Status = status
	state.StatusManual = true
	state.LastActivityAt = time.Now()
	if err := store.SetUserBookState(state); err != nil {
		return nil, err
	}
	return state, nil
}

// RebuildReport describes one RebuildUserBookState run.
type RebuildReport struct {
	UserID        string `json:"user_id"`
	BookID        string `json:"book_id"`
	StateReadable bool   `json:"state_readable"`
	StateError    string `json:"state_error,omitempty"`
	WouldRebuild  bool   `json:"would_rebuild"`
	Applied       bool   `json:"applied"`
	// Rebuilt is the state derived from positions (the preview on a dry run).
	Rebuilt *database.UserBookState `json:"rebuilt,omitempty"`
	// Lost names what an undecodable row carried that positions cannot
	// restore; it is gone either way, the rebuild only makes that explicit.
	Lost []string `json:"lost,omitempty"`
}

// RebuildUserBookState is the repair path for an UNREADABLE user_book_state
// row. Every state write fails closed on such a row (ErrStateUnreadable, 503),
// so without this the book stays unwritable for that user forever.
//
// A readable row is never touched. For an unreadable one it derives a fresh
// state from the positions (exactly as RecomputeUserBookState would for a
// user with no stored state) and, only when apply is true, writes it over the
// bad row. Dry run by default at the call sites. Fields only the lost row
// carried (a manual status, hide-from-continue-listening, the progress-reset
// tombstone) cannot be recovered and are listed in Lost. Positions and files
// must be readable: this never guesses.
func RebuildUserBookState(store Store, userID, bookID string, apply bool) (RebuildReport, error) {
	rep := RebuildReport{UserID: userID, BookID: bookID}
	if store == nil || userID == "" || bookID == "" {
		return rep, fmt.Errorf("readstatus: rebuild needs a store, user and book")
	}
	if _, err := store.GetUserBookState(userID, bookID); err == nil {
		rep.StateReadable = true
		return rep, nil
	} else {
		rep.StateError = err.Error()
	}
	positions, err := store.ListUserPositionsForBook(userID, bookID)
	if err != nil {
		return rep, fmt.Errorf("%w: %s/%s: %w", ErrPositionsUnreadable, userID, bookID, err)
	}
	rebuilt, err := deriveState(store, userID, bookID, nil, positions)
	if err != nil {
		return rep, err
	}
	rep.WouldRebuild = true
	rep.Rebuilt = rebuilt
	rep.Lost = []string{"status_manual", "hide_from_continue_listening", "progress_reset_at", "progress_reset_positions", "finished_at"}
	if !apply {
		return rep, nil
	}
	if err := store.SetUserBookState(rebuilt); err != nil {
		return rep, err
	}
	rep.Applied = true
	return rep, nil
}
