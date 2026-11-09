// file: internal/metafetch/category_tags_test.go
// version: 1.0.1
// guid: d4e5f6a7-b8c9-0d1e-2f3a-4b5c6d7e8f9a

package metafetch

import (
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// TestApplyMetadataCandidate_CategoryTags verifies that category tags from
// a MetadataCandidate are written to book_tags with source="audible_category".
func TestApplyMetadataCandidate_CategoryTags(t *testing.T) {
	bookID := "test-book-id-001"
	title := "Test Book"

	var tagCalls []struct{ tag, source string }

	mock := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			return &database.Book{ID: id, Title: title}, nil
		},
		UpdateBookFunc: func(id string, book *database.Book) (*database.Book, error) {
			return book, nil
		},
		AddBookTagWithSourceFunc: func(bID, tag, source string) error {
			if bID == bookID {
				tagCalls = append(tagCalls, struct{ tag, source string }{tag, source})
			}
			return nil
		},
	}

	svc := NewService(mock)

	candidate := MetadataCandidate{
		Title:        title,
		Author:       "Some Author",
		Source:       "Audible",
		Score:        1.0,
		CategoryTags: []string{"Mystery", "Thriller"},
	}

	_, err := svc.ApplyMetadataCandidate(bookID, candidate, nil)
	if err != nil {
		t.Fatalf("ApplyMetadataCandidate returned error: %v", err)
	}

	// Filter to only audible_category source calls
	var catCalls []string
	for _, c := range tagCalls {
		if c.source == "audible_category" {
			catCalls = append(catCalls, c.tag)
		}
	}

	if len(catCalls) != 2 {
		t.Fatalf("expected 2 audible_category tag calls, got %d: %v", len(catCalls), catCalls)
	}

	want := map[string]bool{"Mystery": true, "Thriller": true}
	for _, tag := range catCalls {
		if !want[tag] {
			t.Errorf("unexpected tag %q in audible_category calls", tag)
		}
		delete(want, tag)
	}
	for tag := range want {
		t.Errorf("expected tag %q was not applied", tag)
	}
}

// TestApplyMetadataCandidate_NoCategoryTags verifies that when CategoryTags
// is empty, AddBookTagWithSource is not called with source="audible_category".
func TestApplyMetadataCandidate_NoCategoryTags(t *testing.T) {
	bookID := "test-book-id-002"

	var catTagCalls int

	mock := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			return &database.Book{ID: id, Title: "No Genres"}, nil
		},
		UpdateBookFunc: func(id string, book *database.Book) (*database.Book, error) {
			return book, nil
		},
		AddBookTagWithSourceFunc: func(bID, tag, source string) error {
			if source == "audible_category" {
				catTagCalls++
			}
			return nil
		},
	}

	svc := NewService(mock)

	candidate := MetadataCandidate{
		Title:  "No Genres",
		Source: "Audible",
		Score:  1.0,
		// CategoryTags intentionally nil
	}

	_, err := svc.ApplyMetadataCandidate(bookID, candidate, nil)
	if err != nil {
		t.Fatalf("ApplyMetadataCandidate returned error: %v", err)
	}

	if catTagCalls != 0 {
		t.Errorf("expected 0 audible_category tag calls, got %d", catTagCalls)
	}
}
