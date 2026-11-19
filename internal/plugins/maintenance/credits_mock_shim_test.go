// file: internal/plugins/maintenance/credits_mock_shim_test.go
// version: 1.0.1
// guid: 3b7d9e12-58a4-4f0c-9c61-2e8a5d704b13
// last-edited: 2026-11-19

package maintenance

import (
	"errors"
	"slices"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// withCredits gives a MockStore the one write the maintenance author merges now
// make per book, database.BookCreditsModifier, composed from the mock's own funcs.
// MockStore does not implement it (and is not edited for this): composing
// GetBookByID, GetBookAuthors/Narrators, UpdateBook and SetBookAuthors/Narrators
// keeps the existing assertions (calls.setAuthors, calls.updated, the journal
// order) observing the write, as ModifyBook and ModifyBookAuthors already do.
//
// Like the mock's ModifyBook it is not atomic and not safe for concurrency; the
// tests that prove atomicity and a lock saved mid-run use a real PebbleStore.
func withCredits(m *database.MockStore) *creditsMock { return &creditsMock{MockStore: m} }

type creditsMock struct {
	*database.MockStore
	// beforeCallback, when set, runs after ModifyBookCredits is entered and
	// before its callback: the instant a lock saved "just now" must still be seen.
	beforeCallback func(bookID string)
}

func (c *creditsMock) ModifyBookCredits(bookID string, fn func(book *database.Book, credits *database.BookCreditsEdit) error) (*database.Book, database.BookCreditsEdit, error) {
	if c.beforeCallback != nil {
		c.beforeCallback(bookID)
	}
	stored, err := c.GetBookByID(bookID)
	if err != nil {
		return nil, database.BookCreditsEdit{}, err
	}
	if stored == nil {
		return nil, database.BookCreditsEdit{}, nil
	}
	book, err := database.SnapshotBook(stored)
	if err != nil {
		return nil, database.BookCreditsEdit{}, err
	}
	authors, err := c.GetBookAuthors(bookID)
	if err != nil {
		return nil, database.BookCreditsEdit{}, err
	}
	narrators, err := c.GetBookNarrators(bookID)
	if err != nil {
		return nil, database.BookCreditsEdit{}, err
	}
	edit := database.BookCreditsEdit{Authors: slices.Clone(authors), Narrators: slices.Clone(narrators)}
	if err := fn(book, &edit); err != nil {
		if errors.Is(err, database.ErrSkipBookWrite) {
			return book, database.BookCreditsEdit{Authors: authors, Narrators: narrators}, nil
		}
		return nil, database.BookCreditsEdit{}, err
	}
	if !slices.Equal(edit.Authors, authors) {
		if err := c.SetBookAuthors(bookID, edit.Authors); err != nil {
			return nil, database.BookCreditsEdit{}, err
		}
	}
	if !slices.Equal(edit.Narrators, narrators) {
		if err := c.SetBookNarrators(bookID, edit.Narrators); err != nil {
			return nil, database.BookCreditsEdit{}, err
		}
	}
	if _, err := c.UpdateBook(bookID, book); err != nil {
		return nil, database.BookCreditsEdit{}, err
	}
	return book, edit, nil
}

// mockStoreOf is the MockStore under a plugin fixture's store, whether or not
// withCredits wrapped it.
func mockStoreOf(s any) *database.MockStore {
	if c, ok := s.(*creditsMock); ok {
		return c.MockStore
	}
	return s.(*database.MockStore)
}

// ModifyBookNarrators completes database.BookCreditsModifier (the capability
// MoveCredits resolves); the author merges never call it.
func (c *creditsMock) ModifyBookNarrators(bookID string, fn func([]database.BookNarrator) ([]database.BookNarrator, error)) ([]database.BookNarrator, error) {
	cur, err := c.GetBookNarrators(bookID)
	if err != nil {
		return nil, err
	}
	next, err := fn(slices.Clone(cur))
	if err != nil {
		return nil, err
	}
	if err := c.SetBookNarrators(bookID, next); err != nil {
		return nil, err
	}
	return next, nil
}

var _ database.BookCreditsModifier = (*creditsMock)(nil)

// listedBookRows answers GetBookByID for every book a fixture lists under an
// author: the real store lists only books that have a row, so a fixture that lists
// a book gives it one (a guarded credit move refuses a book with no row).
func listedBookRows(byAuthor map[int][]database.BookCore) func(string) (*database.Book, error) {
	return func(id string) (*database.Book, error) {
		for _, books := range byAuthor {
			for _, b := range books {
				if b.ID == id {
					return &database.Book{ID: id, Title: id, AuthorID: b.AuthorID}, nil
				}
			}
		}
		return nil, nil
	}
}
