// file: internal/database/book_owned_files_check.go
// version: 1.1.0
// guid: 7a3c9e15-0b64-4d28-8f1e-c5d2a96b4e07
// last-edited: 2026-10-25

package database

import (
	"fmt"
)

// CheckBookOwnsNoFiles is the caller-side prediction of DeleteBook's refusal
// (ErrBookOwnsFiles): it returns an error wrapping ErrBookOwnsFiles when
// bookID still owns book_file rows, and nil when DeleteBook would not refuse
// on them.
//
// It exists so every "does this book own files" check made BEFORE a carry of
// users' listening state (a hard delete that moves the state first, then
// deletes) asks the question DeleteBook asks. DeleteBook counts every key
// under book_file:<id>: (countBookFileRows), retired rows included, while
// GetBookFiles skips retired rows (a retired row is not a file of the book it
// is keyed under; see BookFile.Retired). A check made with GetBookFiles
// passes a book that holds only retired rows, the carry moves its state, and
// DeleteBook then refuses. So this reads GetBookFilesIncludingRetired
// (RetiredBookFileReader), whose rows are those same keys.
//
// It fails closed: a store that cannot list retired rows (no
// RetiredBookFileReader in its decorator chain, as with a bare mock) or a
// read that fails is an error that does NOT wrap ErrBookOwnsFiles, because
// neither is proof that the book owns rows, and neither is proof it owns
// none.
//
// action names what is being refused, for the message ("hard delete", which
// is offered a soft delete as the alternative, or "purge" and "discard
// progress and purge", which act on a book already in the trash); the wording tells a live row (which can be moved to another book)
// from a retired one (which cannot: the generic moves refuse it with
// ErrBookFileRetired).
func CheckBookOwnsNoFiles(store any, bookID, action string) error {
	r, ok := AsCapability[RetiredBookFileReader](store)
	if !ok {
		return fmt.Errorf("%s %s: the store cannot list a book's book_file rows including retired ones, so whether it owns any cannot be checked", action, bookID)
	}
	rows, err := r.GetBookFilesIncludingRetired(bookID)
	if err != nil {
		return fmt.Errorf("%s %s: cannot read its book_file rows: %w", action, bookID, err)
	}
	live, retired := 0, 0
	for i := range rows {
		if rows[i].Retired {
			retired++
		} else {
			live++
		}
	}
	switch {
	case live == 0 && retired == 0:
		return nil
	case retired == 0:
		advice := "move its files to another book first"
		if action == "hard delete" {
			// A book being hard deleted is not in the trash yet; one being
			// purged already is, so soft-deleting it is no advice.
			advice = "soft-delete it instead, or " + advice
		}
		return fmt.Errorf("%s %s: %w (%d row(s)); %s",
			action, bookID, ErrBookOwnsFiles, live, advice)
	case live == 0:
		return fmt.Errorf("%s %s: %w (%d retired row(s) kept for another version of the book; they cannot be moved, so it cannot be deleted while they exist)",
			action, bookID, ErrBookOwnsFiles, retired)
	default:
		return fmt.Errorf("%s %s: %w (%d live and %d retired row(s)); its live files can be moved to another book first, but its retired rows cannot be moved, so it cannot be deleted while they exist",
			action, bookID, ErrBookOwnsFiles, live, retired)
	}
}
