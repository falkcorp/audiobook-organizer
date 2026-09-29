// file: internal/undo/junk_author.go
// version: 1.2.0
// guid: afd8a648-29e9-471a-aa53-2dbf60224fdf
// last-edited: 2026-09-29

package undo

import (
	"encoding/json"
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// ChangeTypeJunkAuthorCredits: the Repairs lane's junk-author fixer moved one
// book's credit off an author row that is not a person. OldValue is a
// TitleRelinkCreditsSnapshot (the junction in order and the primary AuthorID,
// read under the book's author lock immediately before the write); NewValue a
// JunkAuthorCreditsMove, which carries the EXACT junction the fixer wrote.
//
// It is a sibling of ChangeTypeTitleRelinkCredits rather than a reuse because
// the move may UNLINK: a book with no evidence of its real author loses the
// junk credit and gets none (IntoAuthorID 0), which the title-relink decode
// and compare-and-set both refuse.
const ChangeTypeJunkAuthorCredits = "junk_author_credits"

// JunkAuthorCreditsMove is NewValue of a ChangeTypeJunkAuthorCredits row.
type JunkAuthorCreditsMove struct {
	FromAuthorID int `json:"from_author_id"`
	// IntoAuthorID is the real author credited in the junk row's place; 0
	// when the junk credit was removed with no replacement.
	IntoAuthorID int `json:"into_author_id"`
	// CreditsAfter is the junction exactly as the fixer wrote it. The revert
	// restores the snapshot only while the book still holds exactly this: a
	// credit anyone added, removed or re-ordered since is a later change the
	// revert must not overwrite. Required (a pointer so an empty junction,
	// the unlink of a book's only credit, is told apart from a missing one).
	CreditsAfter *[]database.BookAuthor `json:"credits_after"`
	// PrimaryAfter is the primary AuthorID the fixer wrote when the book's
	// primary was the junk row (nil when it cleared it); nil with
	// PrimaryChanged false when the primary was left alone.
	PrimaryAfter   *int `json:"primary_after,omitempty"`
	PrimaryChanged bool `json:"primary_changed,omitempty"`
}

// DecodeJunkAuthorCredits parses a ChangeTypeJunkAuthorCredits row. A row
// without CreditsAfter is refused: the revert could not tell a later change
// from the fixer's own write.
func DecodeJunkAuthorCredits(c *database.OperationChange) (TitleRelinkCreditsSnapshot, JunkAuthorCreditsMove, error) {
	var snap TitleRelinkCreditsSnapshot
	var move JunkAuthorCreditsMove
	if err := json.Unmarshal([]byte(c.OldValue), &snap); err != nil {
		return snap, move, &ReferentError{Reason: ReasonOldValueUnparsable, Detail: fmt.Sprintf("credits snapshot of change %s: %v", c.ID, err)}
	}
	if err := json.Unmarshal([]byte(c.NewValue), &move); err != nil || move.FromAuthorID <= 0 || move.IntoAuthorID < 0 || move.CreditsAfter == nil {
		return snap, move, &ReferentError{Reason: ReasonOldValueUnparsable, Detail: fmt.Sprintf("credit move of change %s", c.ID)}
	}
	return snap, move, nil
}

// SameJunction reports whether two junctions are the same credits in the same
// order with the same roles.
func SameJunction(a, b []database.BookAuthor) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].AuthorID != b[i].AuthorID || a[i].Role != b[i].Role || a[i].Position != b[i].Position {
			return false
		}
	}
	return true
}

// CheckJunkAuthorCreditsCurrent is the revert's compare-and-set on the
// junction: the book must hold EXACTLY the junction the fixer wrote. Anything
// else -- a credit fixed by hand, a co-author added, the junk row re-added --
// is a later change, which the revert must not overwrite.
func CheckJunkAuthorCreditsCurrent(bookID string, current []database.BookAuthor, move JunkAuthorCreditsMove) error {
	if move.CreditsAfter == nil || !SameJunction(current, *move.CreditsAfter) {
		return refuse(ReasonChangedSince, "book %s credits changed since the junk-author repair", bookID)
	}
	return nil
}

// CheckJunkAuthorPrimaryCurrent is the compare-and-set on the primary, run
// BEFORE the junction is touched so a refused primary never leaves a
// half-reverted book. It returns ErrAlreadyRestored when the primary already
// holds the snapshot's value.
func CheckJunkAuthorPrimaryCurrent(bookID string, current *int, snap TitleRelinkCreditsSnapshot, move JunkAuthorCreditsMove) error {
	if !move.PrimaryChanged {
		return nil
	}
	switch {
	case sameIntPointer(current, move.PrimaryAfter):
		return nil
	case sameIntPointer(current, snap.AuthorID):
		return ErrAlreadyRestored
	}
	return refuse(ReasonChangedSince, "book %s primary author changed since the junk-author repair", bookID)
}

// ChangeTypeJunkAuthorCreate: an author row the junk-author fixer created as
// a relink target. NewValue is a JunkAuthorCreate naming the row by ID: the
// row is journaled AFTER CreateAuthor returned it, so the revert deletes only
// the row this op made, never a same-named row someone else created (the
// title-relink create type names the row by name, which cannot tell them
// apart). A crash between the create and this row leaves an uncredited
// author, which maintenance.purge-empty-authors removes.
const ChangeTypeJunkAuthorCreate = "junk_author_create"

// JunkAuthorCreate is NewValue of a ChangeTypeJunkAuthorCreate row.
type JunkAuthorCreate struct {
	AuthorID int    `json:"author_id"`
	Name     string `json:"name"`
}

// DecodeJunkAuthorCreate parses a ChangeTypeJunkAuthorCreate row.
func DecodeJunkAuthorCreate(c *database.OperationChange) (JunkAuthorCreate, error) {
	var v JunkAuthorCreate
	if err := json.Unmarshal([]byte(c.NewValue), &v); err != nil || v.AuthorID <= 0 {
		return v, &ReferentError{Reason: ReasonOldValueUnparsable, Detail: fmt.Sprintf("created author of change %s", c.ID)}
	}
	return v, nil
}

func sameIntPointer(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
