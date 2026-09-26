// file: internal/undo/title_relink.go
// version: 1.0.0
// guid: a5877267-eebe-4c7a-a27b-4e54da1cb6b7
// last-edited: 2026-09-26

package undo

import (
	"encoding/json"
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Journal rows written by maintenance.author-strip-merge's title-as-author
// relink, each BEFORE the write it describes (the journal-first decision of
// 2026-09-26; safe because the row holds the OLD value, so a row whose write
// never happened reverts to what the book still has).
const (
	// ChangeTypeTitleRelinkCredits: one book's credit moved from a junk
	// author row to its real author. OldValue is a TitleRelinkCreditsSnapshot
	// (the book's credits before the move); NewValue a TitleRelinkCreditsMove.
	ChangeTypeTitleRelinkCredits = "title_relink_credits"
	// ChangeTypeTitleRelinkAuthorCreate: an author row the relink created
	// because no row had the name. NewValue is the name, OldValue "". The id
	// is not known when the row is journaled; the revert finds the author by
	// name.
	ChangeTypeTitleRelinkAuthorCreate = "title_relink_author_create"
)

// TitleRelinkCreditsSnapshot is a book's credits as a
// ChangeTypeTitleRelinkCredits row stores them: the junction in order, with
// roles and co-authors, and the primary AuthorID.
type TitleRelinkCreditsSnapshot struct {
	AuthorID *int                  `json:"author_id"`
	Credits  []database.BookAuthor `json:"credits"`
}

// TitleRelinkCreditsMove is NewValue of a ChangeTypeTitleRelinkCredits row.
type TitleRelinkCreditsMove struct {
	FromAuthorID int `json:"from_author_id"`
	IntoAuthorID int `json:"into_author_id"`
}

// DecodeTitleRelinkCredits parses a ChangeTypeTitleRelinkCredits row.
func DecodeTitleRelinkCredits(c *database.OperationChange) (TitleRelinkCreditsSnapshot, TitleRelinkCreditsMove, error) {
	var snap TitleRelinkCreditsSnapshot
	var move TitleRelinkCreditsMove
	if err := json.Unmarshal([]byte(c.OldValue), &snap); err != nil {
		return snap, move, &ReferentError{Reason: ReasonOldValueUnparsable, Detail: fmt.Sprintf("credits snapshot of change %s: %v", c.ID, err)}
	}
	if err := json.Unmarshal([]byte(c.NewValue), &move); err != nil || move.IntoAuthorID <= 0 {
		return snap, move, &ReferentError{Reason: ReasonOldValueUnparsable, Detail: fmt.Sprintf("credit move of change %s", c.ID)}
	}
	return snap, move, nil
}

// CheckTitleRelinkCreditsCurrent is the revert's compare-and-set: the book's
// current junction must still carry the real author the relink wrote and not
// the junk row it removed. Anything else is a later change, which the revert
// must not overwrite.
func CheckTitleRelinkCreditsCurrent(bookID string, current []database.BookAuthor, move TitleRelinkCreditsMove) error {
	hasInto, hasFrom := false, false
	for _, ba := range current {
		hasInto = hasInto || ba.AuthorID == move.IntoAuthorID
		hasFrom = hasFrom || ba.AuthorID == move.FromAuthorID
	}
	if !hasInto || hasFrom {
		return refuse(ReasonChangedSince, "book %s credits changed since the relink", bookID)
	}
	return nil
}
