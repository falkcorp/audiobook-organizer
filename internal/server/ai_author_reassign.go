// file: internal/server/ai_author_reassign.go
// version: 1.0.0
// guid: 4e8c1b53-9a27-4d60-bf14-7c9e0a35d2f1
// last-edited: 2026-07-16

package server

import (
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// authorReassignStore is the minimal store surface reassignBooksFromAuthor needs.
// *database.PebbleStore (and the full database.Store) satisfy it.
type authorReassignStore interface {
	GetBooksByAuthorIDWithRoleCore(authorID int) ([]database.BookCore, error)
	GetBookAuthors(bookID string) ([]database.BookAuthor, error)
	SetBookAuthors(bookID string, authors []database.BookAuthor) error
}

// reassignBooksFromAuthor re-points every book currently crediting mergeID so it
// credits keepID instead (de-duplicating when a book already credits keepID).
//
// It returns a slice of per-book error strings. An EMPTY slice means every book
// that credited mergeID was successfully reassigned, so mergeID is safe to delete.
// A NON-EMPTY slice means at least one book still credits mergeID — deleting
// mergeID would leave a dangling BookAuthor row (lost author attribution), so the
// caller MUST skip the delete. This is the fix for the prior code, which deleted
// mergeID unconditionally even when a book's reassignment (or its author read)
// had failed.
func reassignBooksFromAuthor(store authorReassignStore, mergeID, keepID int) []string {
	books, err := store.GetBooksByAuthorIDWithRoleCore(mergeID)
	if err != nil {
		return []string{fmt.Sprintf("get books for author %d: %v", mergeID, err)}
	}

	var errs []string
	for _, book := range books {
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
		if err := store.SetBookAuthors(book.ID, newAuthors); err != nil {
			errs = append(errs, fmt.Sprintf("update book %s: %v", book.ID, err))
		}
	}
	return errs
}
