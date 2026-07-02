// file: internal/server/isbn_enrichment_test.go
// version: 1.2.1
// guid: 5b7766bc-1f00-4f32-b8ca-8cb0e815c9a1
// last-edited: 2026-07-01

package server

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// stubSource is a minimal MetadataSource for testing ISBN enrichment.
type stubSource struct {
	name    string
	results []metadata.BookMetadata
}

func (s *stubSource) Name() string { return s.name }
func (s *stubSource) SearchByTitle(_ context.Context, _ string) ([]metadata.BookMetadata, error) {
	return s.results, nil
}
func (s *stubSource) SearchByTitleAndAuthor(_ context.Context, _, _ string) ([]metadata.BookMetadata, error) {
	return s.results, nil
}

func TestEnrichBookISBN_SkipsWhenAlreadyHasISBN(t *testing.T) {
	isbn13 := "9780140328721"
	mock := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			return &database.Book{ID: id, Title: "Test Book", ISBN13: &isbn13}, nil
		},
	}
	svc := metafetch.NewISBNService(mock, nil)
	found, err := svc.EnrichBookISBN(context.Background(), "book-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Error("expected found=false when ISBN already present")
	}
}

func TestEnrichBookISBN_FindsISBN13(t *testing.T) {
	var updatedBook *database.Book
	mock := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			return &database.Book{ID: id, Title: "The Great Gatsby"}, nil
		},
		UpdateBookFunc: func(id string, book *database.Book) (*database.Book, error) {
			updatedBook = book
			return book, nil
		},
	}
	src := &stubSource{
		name: "Open Library",
		results: []metadata.BookMetadata{
			{Title: "The Great Gatsby", ISBN: "9780743273565"},
		},
	}
	svc := metafetch.NewISBNService(mock, []metadata.MetadataSource{src})
	found, err := svc.EnrichBookISBN(context.Background(), "book-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	if updatedBook == nil || updatedBook.ISBN13 == nil || *updatedBook.ISBN13 != "9780743273565" {
		t.Error("expected ISBN13 to be set to 9780743273565")
	}
}

func TestEnrichBookISBN_FindsISBN10(t *testing.T) {
	var updatedBook *database.Book
	mock := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			return &database.Book{ID: id, Title: "Dune"}, nil
		},
		UpdateBookFunc: func(id string, book *database.Book) (*database.Book, error) {
			updatedBook = book
			return book, nil
		},
	}
	src := &stubSource{
		name: "Google Books",
		results: []metadata.BookMetadata{
			{Title: "Dune", ISBN: "0441172717"},
		},
	}
	svc := metafetch.NewISBNService(mock, []metadata.MetadataSource{src})
	found, err := svc.EnrichBookISBN(context.Background(), "book-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	if updatedBook == nil || updatedBook.ISBN10 == nil || *updatedBook.ISBN10 != "0441172717" {
		t.Error("expected ISBN10 to be set to 0441172717")
	}
}

func TestEnrichBookISBN_FindsASIN(t *testing.T) {
	var updatedBook *database.Book
	isbn := "9780000000000"
	mock := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			return &database.Book{ID: id, Title: "Neuromancer", ISBN13: &isbn}, nil
		},
		UpdateBookFunc: func(id string, book *database.Book) (*database.Book, error) {
			updatedBook = book
			return book, nil
		},
	}
	src := &stubSource{
		name: "Audible",
		results: []metadata.BookMetadata{
			{Title: "Neuromancer", ASIN: "B000SEGUDE"},
		},
	}
	svc := metafetch.NewISBNService(mock, []metadata.MetadataSource{src})
	found, err := svc.EnrichBookISBN(context.Background(), "book-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true for ASIN enrichment")
	}
	if updatedBook == nil || updatedBook.ASIN == nil || *updatedBook.ASIN != "B000SEGUDE" {
		t.Error("expected ASIN to be set to B000SEGUDE")
	}
}

func TestEnrichBookISBN_NoResultsReturnsFalse(t *testing.T) {
	mock := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			return &database.Book{ID: id, Title: "Obscure Unknown Book"}, nil
		},
	}
	src := &stubSource{
		name:    "Open Library",
		results: nil,
	}
	svc := metafetch.NewISBNService(mock, []metadata.MetadataSource{src})
	found, err := svc.EnrichBookISBN(context.Background(), "book-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Error("expected found=false when no results")
	}
}

func TestEnrichBookISBN_StrictTitleMismatchSkips(t *testing.T) {
	mock := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			return &database.Book{ID: id, Title: "Dune"}, nil
		},
	}
	src := &stubSource{
		name: "Open Library",
		results: []metadata.BookMetadata{
			{Title: "Dune Messiah", ISBN: "9780441172696"},
		},
	}
	svc := metafetch.NewISBNService(mock, []metadata.MetadataSource{src})
	found, err := svc.EnrichBookISBN(context.Background(), "book-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Fatal("expected enrichment to skip weak prefix match")
	}
}

func TestIsStrictTitleMatch(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"Dune", "Dune", true},
		{"dune", "DUNE", true},
		{"  Dune  ", "Dune", true},
		{"Shadows of Self", "Shadows of Self: A Mistborn Novel", true},
		{"Dune", "Dune Messiah", false},
		{"Gatsby", "The Great Gatsby", false},
		{"Completely Different", "Unrelated Book", false},
	}
	for _, tt := range tests {
		got := metafetch.IsStrictTitleMatch(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("metafetch.IsStrictTitleMatch(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestEnrichMissingISBNs_RespectsLimit(t *testing.T) {
	checkedIDs := make([]string, 0)
	books := []database.Book{
		{ID: "book-1", Title: "One"},
		{ID: "book-2", Title: "Two"},
		{ID: "book-3", Title: "Three"},
	}
	mock := &database.MockStore{
		GetAllBooksFunc: func(limit, offset int) ([]database.Book, error) {
			if offset > 0 {
				return nil, nil
			}
			return books, nil
		},
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			for i := range books {
				if books[i].ID == id {
					book := books[i]
					return &book, nil
				}
			}
			return nil, nil
		},
		UpdateBookFunc: func(id string, book *database.Book) (*database.Book, error) {
			checkedIDs = append(checkedIDs, id)
			return book, nil
		},
	}
	src := &stubSource{
		name: "Open Library",
		results: []metadata.BookMetadata{
			{Title: "One", ISBN: "9780000000001"},
			{Title: "Two", ISBN: "9780000000002"},
			{Title: "Three", ISBN: "9780000000003"},
		},
	}
	svc := metafetch.NewISBNService(mock, []metadata.MetadataSource{src})

	checked, updated, err := svc.EnrichMissingISBNs(context.Background(), 2, nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if checked != 2 {
		t.Fatalf("expected 2 checked books, got %d", checked)
	}
	if updated != 2 {
		t.Fatalf("expected 2 updated books, got %d", updated)
	}
	if len(checkedIDs) != 2 {
		t.Fatalf("expected 2 updated IDs, got %d", len(checkedIDs))
	}
}
