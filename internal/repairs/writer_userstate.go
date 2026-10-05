// file: internal/repairs/writer_userstate.go
// version: 1.2.0
// guid: 7c7635eb-815c-443d-830a-34ca2ca14c4a
// last-edited: 2026-10-05

package repairs

import (
	"context"
	"errors"
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// A fixer that writes a user's listening state (maintenance.audible-read-status)
// writes user_book_state and position rows, which neither metadata history
// nor the book row holds. Every such write is journaled first through the
// Writer's op journal as undo.ChangeTypeUserBookStateSet under the apply op's
// id, with the user's whole state and positions before and after, so the op
// revert (POST /operations/:id/revert) puts them back while they still hold
// what the apply wrote, and refuses once the user has moved on.
//
// The store has no compare-and-set for user state, so SetUserState holds
// database.LockUserBookState (shared with the ABS write paths and the
// revert) across re-reading the state, comparing it with what the fixer
// decided on, journaling and writing. Writers that do not take that lock yet
// are listed on it.

// UserStateStore is the user listening-state surface Writer offers a fixer.
// It has no delete method: an apply only creates or changes rows (the revert
// deletes a row the apply created).
type UserStateStore interface {
	undo.UserStateReader
	SetUserBookState(state *database.UserBookState) error
	// SetUserPositionAt keeps the caller's UpdatedAt: an imported position
	// must carry the source's time, never "now" (which is also the ABS
	// lastUpdate and would beat a real, later device sync).
	database.UserPositionTimestampWriter
}

// WithUserState wires w to write user listening state through store.
// Without WithJournal, SetUserState fails with ErrNotJournaled.
func (w *Writer) WithUserState(store UserStateStore) *Writer {
	w.userState = store
	return w
}

// SetUserState moves userID's state on bookID from expect to next: next.State
// is written (never nil: an apply does not delete a state) and every position
// of next that expect does not hold is written with its own UpdatedAt. The
// user's current state and positions are read first and must equal expect
// (undo.SameUserBookState, which ignores the row's UpdatedAt, and
// undo.SameUserPositions); otherwise nothing is written and the error wraps
// ErrChangedSincePlan. The change is journaled before the write.
//
// Positions are written before the state. A position write that fails leaves
// nothing written and voids the journal row. A state write that fails after
// positions were written keeps the row (the revert's per-part check puts the
// positions back) and the error wraps ErrPartiallyApplied.
func (w *Writer) SetUserState(ctx context.Context, userID, bookID string, expect, next undo.UserStateSnapshot) error {
	if err := w.denyTagsOnly("SetUserState"); err != nil {
		return err
	}
	if w.userState == nil {
		return errors.New("repairs: writer has no user state store")
	}
	if userID == "" || bookID == "" || next.State == nil {
		return fmt.Errorf("repairs: user, book and the new state are required (user %q, book %q)", userID, bookID)
	}
	if next.State.UserID != userID || next.State.BookID != bookID {
		return fmt.Errorf("repairs: new state names %s/%s, not %s/%s", next.State.UserID, next.State.BookID, userID, bookID)
	}
	if err := w.beat("listening state of " + userID + " on book " + bookID); err != nil {
		return err
	}
	// The read, the compare and the write are one step against the other
	// user-state writers. The merge lock first (a merge follow moves user
	// state under it, and the revert of these rows holds it), then the
	// per-(user, book) stripe the ABS paths hold: the same order as the
	// revert, never the reverse. LockWaiting keeps the scan stand-down
	// lease alive while it waits for the merge lock.
	if err := w.LockWaiting(ctx, "the merge lock for "+userID+" on "+bookID, merge.LockMergeRMW, merge.UnlockMergeRMW); err != nil {
		return err
	}
	defer merge.UnlockMergeRMW()
	defer database.LockUserBookState(userID, bookID)()
	cur, err := undo.ReadUserStateSnapshot(w.userState, userID, bookID)
	if err != nil {
		return err
	}
	if !undo.SameUserBookState(cur.State, expect.State) || !undo.SameUserPositions(cur.Positions, expect.Positions) {
		return fmt.Errorf("%w: the listening state of %s on %s changed since it was planned", ErrChangedSincePlan, userID, bookID)
	}
	// Journal the state as read, not as planned: they are equal on every
	// compared field, and the read one carries the row's real UpdatedAt.
	oldV, err := undo.EncodeUserStateSnapshot(cur)
	if err != nil {
		return err
	}
	newV, err := undo.EncodeUserStateSnapshot(next)
	if err != nil {
		return err
	}
	add := newPositions(cur.Positions, next.Positions)
	for _, p := range add {
		if p.UserID != userID || p.BookID != bookID {
			return fmt.Errorf("repairs: position names %s/%s, not %s/%s", p.UserID, p.BookID, userID, bookID)
		}
		if p.UpdatedAt.IsZero() {
			return fmt.Errorf("repairs: position %s on %s has no timestamp; refusing to stamp it now", p.SegmentID, bookID)
		}
	}
	// partial is set when something was written before a later write
	// failed: the closure then returns nil so JournalStep keeps the row.
	var partial error
	err = w.JournalStep(bookID, UndoEntry{ChangeType: undo.ChangeTypeUserBookStateSet, Field: undo.UserStateField(userID), Old: oldV, New: newV},
		func() error {
			for i, p := range add {
				if perr := w.userState.SetUserPositionAt(userID, bookID, p.SegmentID, p.PositionSeconds, p.UpdatedAt); perr != nil {
					if i == 0 {
						return perr
					}
					partial = fmt.Errorf("%w: position %s of %s on %s: %v", ErrPartiallyApplied, p.SegmentID, userID, bookID, perr)
					return nil
				}
			}
			st := *next.State
			if serr := w.userState.SetUserBookState(&st); serr != nil {
				if len(add) == 0 {
					return serr
				}
				partial = fmt.Errorf("%w: positions of %s on %s written, state not: %v", ErrPartiallyApplied, userID, bookID, serr)
			}
			return nil
		})
	if err != nil {
		return err
	}
	if partial != nil {
		return partial
	}
	w.writes.Add(1)
	return nil
}

// newPositions returns the positions of next that have holds no identical
// row of, in next's order.
func newPositions(have, next []database.UserPosition) []database.UserPosition {
	var out []database.UserPosition
	for _, p := range next {
		found := false
		for _, h := range have {
			if undo.SameUserPositions([]database.UserPosition{h}, []database.UserPosition{p}) {
				found = true
				break
			}
		}
		if !found {
			out = append(out, p)
		}
	}
	return out
}
