// file: internal/server/ai_author_reassign.go
// version: 1.2.0
// guid: 4e8c1b53-9a27-4d60-bf14-7c9e0a35d2f1
// last-edited: 2026-10-14

package server

import (
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/authormerge"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// authorReassignStore is the minimal store surface reassignBooksFromAuthor needs.
// *database.PebbleStore (and the full database.Store) satisfy it.
type authorReassignStore interface {
	authormerge.CreditsStore
	GetBooksByAuthorIDForRelinkCore(authorID int) ([]database.BookCore, error)
}

// reassignBooksFromAuthor re-points every book currently crediting mergeID so it
// credits keepID instead (de-duplicating when a book already credits keepID),
// and repoints the book's primary when it names mergeID.
//
// Each book's move is journaled under opID BEFORE it is made, through
// authormerge.MoveCredits, as the author_merge_credits row the revert engine
// reverses: the same row the Authors view merge writes, so undoing the AI apply
// puts every book's credits and primary back. A book whose row cannot be
// recorded is not moved.
//
// problems are moves that landed but whose undo row could not be brought up to
// date (authormerge.MoveResult.Problems): reported, but they do not hold the
// author back.
//
// It returns a slice of per-book error strings. An EMPTY slice means every book
// that credited mergeID was successfully reassigned, so mergeID is safe to delete.
// A NON-EMPTY slice means at least one book still credits mergeID — deleting
// mergeID would leave a dangling BookAuthor row (lost author attribution), so the
// caller MUST skip the delete. This is the fix for the prior code, which deleted
// mergeID unconditionally even when a book's reassignment (or its author read)
// had failed.
func reassignBooksFromAuthor(store authorReassignStore, opID string, mergeID, keepID int) (errs, problems []string) {
	// ForRelink includes the trash: the caller deletes mergeID afterwards,
	// and DeleteAuthor sweeps trashed books' junction rows too.
	books, err := store.GetBooksByAuthorIDForRelinkCore(mergeID)
	if err != nil {
		return []string{fmt.Sprintf("get books for author %d: %v", mergeID, err)}, nil
	}

	// A book carrying two role rows for mergeID comes back once per row; it is
	// moved (and journaled) once.
	seen := make(map[string]struct{}, len(books))
	for _, book := range books {
		if _, dup := seen[book.ID]; dup {
			continue
		}
		seen[book.ID] = struct{}{}
		bookAuthors, err := store.GetBookAuthors(book.ID)
		if err != nil {
			// Could not read this book's authors → we cannot safely strip mergeID
			// from it, so record the failure (blocks the delete) instead of the
			// prior silent `continue`.
			errs = append(errs, fmt.Sprintf("get authors for book %s: %v", book.ID, err))
			continue
		}
		hasKeep := false
		for _, ba := range bookAuthors {
			if ba.AuthorID == keepID {
				hasKeep = true
				break
			}
		}
		var newAuthors []database.BookAuthor
		for _, ba := range bookAuthors {
			if ba.AuthorID == mergeID {
				if !hasKeep {
					ba.AuthorID = keepID
					newAuthors = append(newAuthors, ba)
					hasKeep = true
				}
			} else {
				newAuthors = append(newAuthors, ba)
			}
		}
		moved, err := authormerge.MoveCredits(store, opID, authormerge.CreditMove{
			BookID: book.ID, FromID: mergeID, IntoID: keepID,
			Before: bookAuthors, After: newAuthors, PrimaryBefore: book.AuthorID,
		})
		if err != nil {
			errs = append(errs, fmt.Sprintf("update book %s: %v", book.ID, err))
			continue
		}
		if moved.PrimaryErr != nil {
			// The credits moved but the book still names mergeID as its primary.
			errs = append(errs, fmt.Sprintf("book %s: credits moved but its primary author could not be repointed: %v", book.ID, moved.PrimaryErr))
			continue
		}
		for _, problem := range moved.Problems {
			problems = append(problems, fmt.Sprintf("book %s: %s", book.ID, problem))
		}
	}
	return errs, problems
}
