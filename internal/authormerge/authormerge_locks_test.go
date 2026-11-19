// file: internal/authormerge/authormerge_locks_test.go
// version: 1.0.0
// guid: 6f0d3b7e-2c91-4a58-b3e6-91d84a07c5f2
// last-edited: 2026-11-18

package authormerge

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// lockReader answers the field-lock read of one book: locked reports the author
// locked, err fails the read, reads counts them.
type lockReader struct {
	locked bool
	err    error
	reads  int
}

func (l *lockReader) GetMetadataFieldStates(bookID string) ([]database.MetadataFieldState, error) {
	l.reads++
	if l.err != nil {
		return nil, l.err
	}
	if !l.locked {
		return nil, nil
	}
	return []database.MetadataFieldState{{BookID: bookID, Field: database.FieldKeyAuthorName, OverrideLocked: true}}, nil
}

func (l *lockReader) GetUserPreference(string) (*database.UserPreference, error) { return nil, nil }

// lockedWorld is book "b" whose primary names author 2 and whose junction credits
// author 2 (primary) and co-author 5.
func lockedWorld() (*creditsFake, database.BookAuthor, CreditMove) {
	f := &creditsFake{fakeStore: newFake()}
	two := 2
	f.books["b"] = &database.Book{ID: "b", AuthorID: &two}
	before := []database.BookAuthor{credit("b", 2, 0), credit("b", 5, 1)}
	f.junction["b"] = slices.Clone(before)
	after := []database.BookAuthor{credit("b", 5, 0), credit("b", 1, 1)}
	return f, credit("b", 5, 1), CreditMove{BookID: "b", FromID: 2, IntoID: 1, Before: before, After: after, PrimaryBefore: &two}
}

func requireNothingMoved(t *testing.T, f *creditsFake, m CreditMove) {
	t.Helper()
	if !undo.SameJunction(f.junction["b"], m.Before) {
		t.Fatalf("the credits moved: %+v", f.junction["b"])
	}
	if f.books["b"].AuthorID == nil || *f.books["b"].AuthorID != 2 {
		t.Fatalf("the primary moved: %+v", f.books["b"].AuthorID)
	}
}

// A book whose author the user locked is left exactly as it is, and the row
// journaled for the move is voided: a row that stands must describe a write that
// happened.
func TestMoveCredits_LocksHoldALockedBookAndVoidTheRow(t *testing.T) {
	f, _, m := lockedWorld()
	m.Locks = &lockReader{locked: true}
	_, err := MoveCredits(f, "op", m)
	if !errors.Is(err, ErrBookLocked) {
		t.Fatalf("err = %v, want ErrBookLocked", err)
	}
	if !f.only(t).Voided {
		t.Fatal("the row of a held move must be voided")
	}
	requireNothingMoved(t, f, m)
}

// The co-author case: the junk row is not the primary, so no primary is
// repointed and a book-column check would see nothing to hold. The credit list
// itself is held by the author lock.
func TestMoveCredits_LocksHoldACoAuthorMoveToo(t *testing.T) {
	f := &creditsFake{fakeStore: newFake()}
	nine := 9
	f.books["b"] = &database.Book{ID: "b", AuthorID: &nine}
	before := []database.BookAuthor{credit("b", 9, 0), credit("b", 2, 1)}
	f.junction["b"] = slices.Clone(before)
	m := CreditMove{BookID: "b", FromID: 2, IntoID: 1, Before: before,
		After: []database.BookAuthor{credit("b", 9, 0), credit("b", 1, 1)}, PrimaryBefore: &nine, Locks: &lockReader{locked: true}}
	_, err := MoveCredits(f, "op", m)
	if !errors.Is(err, ErrBookLocked) {
		t.Fatalf("err = %v, want ErrBookLocked", err)
	}
	if !undo.SameJunction(f.junction["b"], before) {
		t.Fatalf("a locked book's co-author credit moved: %+v", f.junction["b"])
	}
	if !f.only(t).Voided {
		t.Fatal("the row of a held move must be voided")
	}
}

// The control: the same moves with the author unlocked land, through
// ModifyBookCredits, so the two held cases above are not a move that never runs.
func TestMoveCredits_LocksLetAnUnlockedBookMove(t *testing.T) {
	f, _, m := lockedWorld()
	reader := &lockReader{}
	m.Locks = reader
	res, err := MoveCredits(f, "op", m)
	if err != nil || !res.PrimaryRepointed {
		t.Fatalf("res %+v err %v", res, err)
	}
	if reader.reads != 1 {
		t.Fatalf("%d lock reads, want 1 (inside the commit)", reader.reads)
	}
	if *f.books["b"].AuthorID != 1 || !undo.SameJunction(f.junction["b"], m.After) {
		t.Fatalf("not moved: primary %d junction %+v", *f.books["b"].AuthorID, f.junction["b"])
	}
	if f.only(t).Voided {
		t.Fatal("a move that landed must keep its row")
	}

	co := &creditsFake{fakeStore: newFake()}
	nine := 9
	co.books["b"] = &database.Book{ID: "b", AuthorID: &nine}
	before := []database.BookAuthor{credit("b", 9, 0), credit("b", 2, 1)}
	co.junction["b"] = slices.Clone(before)
	after := []database.BookAuthor{credit("b", 9, 0), credit("b", 1, 1)}
	res, err = MoveCredits(co, "op", CreditMove{BookID: "b", FromID: 2, IntoID: 1, Before: before, After: after, PrimaryBefore: &nine, Locks: &lockReader{}})
	if err != nil || res.PrimaryRepointed {
		t.Fatalf("res %+v err %v", res, err)
	}
	if co.calls != 1 || !undo.SameJunction(co.junction["b"], after) || *co.books["b"].AuthorID != 9 {
		t.Fatalf("calls %d junction %+v primary %d", co.calls, co.junction["b"], *co.books["b"].AuthorID)
	}
}

// A lock saved after MoveCredits was called but before the store ran its
// callback still holds the move: the read is inside the commit, not before it.
func TestMoveCredits_ALockSavedBeforeTheCommitHoldsTheMove(t *testing.T) {
	f, _, m := lockedWorld()
	reader := &lockReader{}
	m.Locks = reader
	f.beforeCallback = func() { reader.locked = true }
	if _, err := MoveCredits(f, "op", m); !errors.Is(err, ErrBookLocked) {
		t.Fatalf("err = %v, want ErrBookLocked", err)
	}
	if !f.only(t).Voided {
		t.Fatal("the row of a held move must be voided")
	}
	requireNothingMoved(t, f, m)
}

// An unreadable lock set fails closed: nothing is written, the row is voided and
// the error is not reported as a hold.
func TestMoveCredits_UnreadableLocksWriteNothingAndAreNotAHold(t *testing.T) {
	f, _, m := lockedWorld()
	m.Locks = &lockReader{err: errors.New("field states: store closed")}
	_, err := MoveCredits(f, "op", m)
	if !errors.Is(err, database.ErrFieldLocksUnavailable) || errors.Is(err, ErrBookLocked) {
		t.Fatalf("err = %v, want ErrFieldLocksUnavailable and not ErrBookLocked", err)
	}
	if !f.only(t).Voided {
		t.Fatal("the row of a refused move must be voided")
	}
	requireNothingMoved(t, f, m)
}

// A store that cannot write the row and the credits in one commit is refused
// before anything is journaled: no fallback to a credit write with a window
// before it.
func TestMoveCredits_LocksWithoutTheCapabilityAreRefusedBeforeJournaling(t *testing.T) {
	f := newFake()
	two := 2
	f.books["b"] = &database.Book{ID: "b", AuthorID: &two}
	before := []database.BookAuthor{credit("b", 2, 0)}
	f.junction["b"] = slices.Clone(before)
	_, err := MoveCredits(f, "op", CreditMove{BookID: "b", FromID: 2, IntoID: 1, Before: before,
		After: []database.BookAuthor{credit("b", 1, 0)}, PrimaryBefore: &two, Locks: &lockReader{}})
	if !errors.Is(err, database.ErrBookCreditsUnsupported) {
		t.Fatalf("err = %v, want ErrBookCreditsUnsupported", err)
	}
	if len(f.order) != 0 || f.sets != 0 || !undo.SameJunction(f.junction["b"], before) {
		t.Fatalf("rows %d sets %d junction %+v: nothing may be journaled or written", len(f.order), f.sets, f.junction["b"])
	}
}

// The Authors-view merge sets no Locks: a human merge is not blocked by a lock.
func TestMoveCredits_WithoutLocksALockedBookStillMoves(t *testing.T) {
	f, _, m := lockedWorld()
	res, err := MoveCredits(f, "op", m)
	if err != nil || !res.PrimaryRepointed {
		t.Fatalf("res %+v err %v", res, err)
	}
	if *f.books["b"].AuthorID != 1 {
		t.Fatal("primary not repointed")
	}
}

// A guarded move whose primary moved off FromID since the caller read it moves
// the credits alone in the same commit, and the row is rewritten as "no repoint".
func TestMoveCredits_LocksAPrimaryThatMovedMeanwhileMovesCreditsAlone(t *testing.T) {
	f, _, m := lockedWorld()
	nine := 9
	f.books["b"].AuthorID = &nine
	m.After = []database.BookAuthor{credit("b", 5, 0)}
	m.Locks = &lockReader{}
	res, err := MoveCredits(f, "op", m)
	if err != nil || res.PrimaryRepointed {
		t.Fatalf("res %+v err %v", res, err)
	}
	if f.calls != 1 || *f.books["b"].AuthorID != 9 {
		t.Fatalf("calls %d primary %d: one commit, the other writer's primary stands", f.calls, *f.books["b"].AuthorID)
	}
	if slices.ContainsFunc(f.junction["b"], func(ba database.BookAuthor) bool { return ba.AuthorID == 1 }) {
		t.Fatalf("the keep author was credited on a book whose primary never moved to it: %+v", f.junction["b"])
	}
	var move undo.AuthorCreditsMove
	row := f.only(t)
	if err := json.Unmarshal([]byte(row.NewValue), &move); err != nil {
		t.Fatal(err)
	}
	if move.PrimaryChanged || move.PrimaryAfter != nil {
		t.Fatalf("the row still claims the primary moved: %+v", move)
	}
}

// A guarded move that changes nothing writes nothing.
func TestMoveCredits_LocksAMoveThatChangesNothingWritesNothing(t *testing.T) {
	f := &creditsFake{fakeStore: newFake()}
	nine := 9
	f.books["b"] = &database.Book{ID: "b", AuthorID: &nine}
	j := []database.BookAuthor{credit("b", 9, 0)}
	f.junction["b"] = slices.Clone(j)
	reader := &lockReader{locked: true}
	if _, err := MoveCredits(f, "op", CreditMove{BookID: "b", FromID: 2, IntoID: 1, Before: j, After: j, PrimaryBefore: &nine, Locks: reader}); err != nil {
		t.Fatal(err)
	}
	if f.calls != 0 || reader.reads != 0 {
		t.Fatalf("calls %d reads %d: there was nothing to write or to hold", f.calls, reader.reads)
	}
}
