// file: internal/undo/user_book_state.go
// version: 1.0.0
// guid: a2ec13d4-3bc2-4bd4-a63f-d449e9180dca
// last-edited: 2026-10-05

package undo

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// ChangeTypeUserBookStateSet: a repair wrote one user's listening state for
// one book (the Audible read-status import, maintenance.audible-read-status).
// FieldName is UserStateField(userID). OldValue and NewValue are
// UserStateSnapshot JSON: the user_book_state row (nil for "no row") and the
// user's positions on the book, before and after.
//
// Restorable, part by part, by compare-and-set: the state row is put back
// while it still holds NewValue's state, and the positions while they are
// still exactly NewValue's. A part already back at OldValue counts restored.
// A part that holds anything else -- a device synced a listen, the user
// marked the book, a reset -- is the user's and the whole row is refused:
// nothing is written. The row's UpdatedAt is not compared (the store stamps
// it at write time, after the journal row exists); every field the import
// sets, and every field a later write would move, is.
const ChangeTypeUserBookStateSet = "user_book_state_set"

// UserStateSnapshot is one side of a ChangeTypeUserBookStateSet row.
type UserStateSnapshot struct {
	// State is the user_book_state row; nil means the user had no row.
	State *database.UserBookState `json:"state"`
	// Positions are the user's position rows on the book, any order.
	Positions []database.UserPosition `json:"positions"`
}

// UserStateField is the FieldName of a ChangeTypeUserBookStateSet row.
func UserStateField(userID string) string { return "user:" + userID }

// UserFromStateField parses UserStateField.
func UserFromStateField(field string) (string, bool) {
	id, ok := strings.CutPrefix(field, "user:")
	return id, ok && id != ""
}

// EncodeUserStateSnapshot is the journal form of s.
func EncodeUserStateSnapshot(s UserStateSnapshot) (string, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("encode user state snapshot: %w", err)
	}
	return string(b), nil
}

// DecodeUserStateSnapshot parses a ChangeTypeUserBookStateSet value.
func DecodeUserStateSnapshot(v string) (UserStateSnapshot, error) {
	var s UserStateSnapshot
	if strings.TrimSpace(v) == "" {
		return s, fmt.Errorf("user_book_state_set: empty value")
	}
	if err := json.Unmarshal([]byte(v), &s); err != nil {
		return s, fmt.Errorf("user_book_state_set %q: %w", v, err)
	}
	return s, nil
}

// SameUserBookState reports whether two state rows (nil = no row) hold the
// same listening state. UpdatedAt is not compared (see
// ChangeTypeUserBookStateSet); times compare as instants, so a value that
// went through JSON equals the one written.
func SameUserBookState(a, b *database.UserBookState) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.UserID == b.UserID && a.BookID == b.BookID &&
		a.Status == b.Status && a.StatusManual == b.StatusManual &&
		a.LastActivityAt.Equal(b.LastActivityAt) &&
		sameTimePtr(a.FinishedAt, b.FinishedAt) &&
		a.LastSegmentID == b.LastSegmentID &&
		a.TotalListenedSeconds == b.TotalListenedSeconds &&
		a.ProgressPct == b.ProgressPct &&
		a.HideFromContinueListening == b.HideFromContinueListening &&
		sameTimePtr(a.ProgressResetAt, b.ProgressResetAt) &&
		slices.Equal(a.ProgressResetPositions, b.ProgressResetPositions)
}

// SameUserPositions reports whether two position lists hold the same rows
// (segment, position and UpdatedAt), whatever their order.
func SameUserPositions(a, b []database.UserPosition) bool {
	if len(a) != len(b) {
		return false
	}
	key := func(p database.UserPosition) string {
		return fmt.Sprintf("%s\x00%s\x00%s\x00%v\x00%d", p.UserID, p.BookID, p.SegmentID, p.PositionSeconds, p.UpdatedAt.UnixNano())
	}
	seen := make(map[string]int, len(a))
	for _, p := range a {
		seen[key(p)]++
	}
	for _, p := range b {
		k := key(p)
		if seen[k] == 0 {
			return false
		}
		seen[k]--
	}
	return true
}

func sameTimePtr(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

// UserStateRestore says which parts of a ChangeTypeUserBookStateSet row the
// revert must write back.
type UserStateRestore struct {
	State     bool
	Positions bool
}

// CheckUserBookStateSet is the compare-and-set for a
// ChangeTypeUserBookStateSet row, shared by the revert and the preflight.
// cur is what the user holds now. It returns the parts to restore;
// ErrAlreadyRestored when every part is back at OldValue; a ReasonChangedSince
// refusal when any part holds neither side (nothing is restored then, so a
// half-reverted row never leaves state and positions disagreeing); and a
// ReasonOldValueUnparsable refusal for a row it cannot read.
func CheckUserBookStateSet(cur UserStateSnapshot, c *database.OperationChange) (UserStateRestore, error) {
	var out UserStateRestore
	oldS, err := DecodeUserStateSnapshot(c.OldValue)
	if err != nil {
		return out, refuse(ReasonOldValueUnparsable, "%v", err)
	}
	newS, err := DecodeUserStateSnapshot(c.NewValue)
	if err != nil {
		return out, refuse(ReasonOldValueUnparsable, "%v", err)
	}
	switch {
	case SameUserBookState(cur.State, newS.State) && !SameUserBookState(cur.State, oldS.State):
		out.State = true
	case SameUserBookState(cur.State, oldS.State):
	default:
		return UserStateRestore{}, refuse(ReasonChangedSince,
			"book %s: %s's listening state changed after the import (now %s); it is the user's and is kept",
			c.BookID, c.FieldName, describeUserState(cur.State))
	}
	switch {
	case SameUserPositions(cur.Positions, newS.Positions) && !SameUserPositions(cur.Positions, oldS.Positions):
		out.Positions = true
	case SameUserPositions(cur.Positions, oldS.Positions):
	default:
		return UserStateRestore{}, refuse(ReasonChangedSince,
			"book %s: %s's positions changed after the import (%d now); they are the user's and are kept",
			c.BookID, c.FieldName, len(cur.Positions))
	}
	if !out.State && !out.Positions {
		return out, ErrAlreadyRestored
	}
	return out, nil
}

func describeUserState(s *database.UserBookState) string {
	if s == nil {
		return "no row"
	}
	return fmt.Sprintf("status %q, %d%%, last activity %s", s.Status, s.ProgressPct, s.LastActivityAt.UTC().Format(time.RFC3339))
}

func validUserBookStateSetRow(c *database.OperationChange) bool {
	if _, ok := UserFromStateField(c.FieldName); !ok {
		return false
	}
	if _, err := DecodeUserStateSnapshot(c.OldValue); err != nil {
		return false
	}
	_, err := DecodeUserStateSnapshot(c.NewValue)
	return err == nil
}

// UserStateReader is the store surface that reads a user's listening state
// on a book: the preflight's optional reader, and what the revert and the
// Repairs writer read before their compare-and-set.
type UserStateReader interface {
	GetUserBookState(userID, bookID string) (*database.UserBookState, error)
	ListUserPositionsForBook(userID, bookID string) ([]database.UserPosition, error)
}

// ReadUserStateSnapshot reads what userID holds on bookID now. A read error
// is returned, never read as "no state": the compare-and-set must not treat
// an unreadable row as restorable.
func ReadUserStateSnapshot(r UserStateReader, userID, bookID string) (UserStateSnapshot, error) {
	st, err := r.GetUserBookState(userID, bookID)
	if err != nil {
		return UserStateSnapshot{}, fmt.Errorf("read listening state of %s on %s: %w", userID, bookID, err)
	}
	pos, err := r.ListUserPositionsForBook(userID, bookID)
	if err != nil {
		return UserStateSnapshot{}, fmt.Errorf("read positions of %s on %s: %w", userID, bookID, err)
	}
	return UserStateSnapshot{State: st, Positions: pos}, nil
}
