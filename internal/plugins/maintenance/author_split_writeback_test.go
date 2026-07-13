// file: internal/plugins/maintenance/author_split_writeback_test.go
// version: 1.0.0
// guid: 4b7e1d92-8c6a-4f3b-9a02-1e5c7d8f0a3b
// last-edited: 2026-07-13

package maintenance

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestAuthorSplit_WritesFreshAuthorNotStaleOrNil proves the composite-author
// split write-back (STOREFID W5d-1 / #1887) hydrates the full stored row and
// writes it with BOTH the new AuthorID AND a fresh denormalized Author — never
// a BookCore->ToBook projection (which carries nil Author/Series) and never the
// guard-preserved-but-stale composite Author.
//
// The op changes the book's primary AuthorID from the composite author to the
// first split author, so a merely preserved Author would still name the
// composite (Author.ID != AuthorID). This test fails on both the pre-fix wipe
// (Author == nil) and on a preserve-only fix (Author.ID stale).
func TestAuthorSplit_WritesFreshAuthorNotStaleOrNil(t *testing.T) {
	const (
		compositeID = 1
		firstNewID  = 101
		secondNewID = 102
		seriesID    = 9
	)

	// The stored, hydrated row: has a STALE denormalized Author (the composite)
	// plus a Series and Description that must survive the write.
	desc := "back-cover blurb"
	storedBook := database.Book{
		ID:          "bk1",
		Title:       "The Left Hand of Darkness",
		AuthorID:    intPtr(compositeID),
		SeriesID:    intPtr(seriesID),
		Author:      &database.Author{ID: compositeID, Name: "Alice Smith / Bob Jones"},
		Series:      &database.Series{ID: seriesID, Name: "Hainish Cycle"},
		Description: &desc,
	}

	createCalls := 0
	written := make([]database.Book, 0, 1)

	store := &database.MockStore{
		GetAllAuthorsFunc: func() ([]database.Author, error) {
			return []database.Author{{ID: compositeID, Name: "Alice Smith / Bob Jones"}}, nil
		},
		// Force the create path so newAuthors is populated deterministically.
		GetAuthorByNameFunc: func(_ string) (*database.Author, error) { return nil, nil },
		CreateAuthorFunc: func(name string) (*database.Author, error) {
			createCalls++
			id := firstNewID
			if createCalls == 2 {
				id = secondNewID
			}
			return &database.Author{ID: id, Name: name}, nil
		},
		GetBooksByAuthorIDWithRoleFunc: func(authorID int) ([]database.BookCore, error) {
			if authorID != compositeID {
				return nil, nil
			}
			return []database.BookCore{{ID: "bk1", Title: storedBook.Title, AuthorID: intPtr(compositeID)}}, nil
		},
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			if id == "bk1" {
				b := storedBook // copy
				return &b, nil
			}
			return nil, nil
		},
		UpdateBookFunc: func(_ string, b *database.Book) (*database.Book, error) {
			written = append(written, *b)
			return b, nil
		},
		DeleteAuthorFunc: func(_ int) error { return nil },
	}

	p := New(fakeDeps{store: store})
	if err := p.runAuthorSplitScan(context.Background(), nil, &fakeReporter{}); err != nil {
		t.Fatalf("runAuthorSplitScan: %v", err)
	}

	if len(written) != 1 {
		t.Fatalf("expected exactly 1 UpdateBook write, got %d", len(written))
	}
	got := written[0]

	// AuthorID must advance to the first split author.
	if got.AuthorID == nil || *got.AuthorID != firstNewID {
		t.Errorf("AuthorID: got %v, want %d", got.AuthorID, firstNewID)
	}
	// The denormalized Author must be FRESH (matches new AuthorID), not nil
	// (pre-fix wipe) and not the stale composite (preserve-only).
	if got.Author == nil {
		t.Fatal("Author WIPED: got nil, want fresh denormalized author for the new AuthorID")
	}
	if got.Author.ID != firstNewID {
		t.Errorf("Author STALE: got Author.ID=%d, want %d (must match new AuthorID)", got.Author.ID, firstNewID)
	}
	// Series (unchanged by an author split) must survive via hydration.
	if got.Series == nil || got.Series.ID != seriesID {
		t.Errorf("Series not preserved: got %+v, want ID=%d", got.Series, seriesID)
	}
	// Heavy fields carried by the hydrated row survive too (defense-in-depth).
	if got.Description == nil || *got.Description != desc {
		t.Errorf("Description not preserved: got %v, want %q", got.Description, desc)
	}
}
