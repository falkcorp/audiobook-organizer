// file: internal/metafetch/provider_fields_apply_test.go
// version: 1.0.0
// guid: 6fc4d04d-91c8-4194-b98b-133f4b647ffe
// last-edited: 2026-09-13

package metafetch

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// A Google Books candidate's newly mapped fields reach the Book through the
// real single-book apply (ApplyMetadataCandidate), and its categories are
// tagged as Google provenance, not Audible.
func TestApplyMetadataCandidate_GoogleBooksFieldsReachTheBook(t *testing.T) {
	const bookID = "gb-book-1"
	release := 2019
	var saved *database.Book
	tags := map[string]string{}

	mock := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			return &database.Book{ID: id, Title: "Dragons", AudiobookReleaseYear: &release}, nil
		},
		UpdateBookFunc: func(id string, b *database.Book) (*database.Book, error) {
			saved = b
			return b, nil
		},
		AddBookTagWithSourceFunc: func(_, tag, source string) error {
			tags[tag] = source
			return nil
		},
	}
	svc := NewService(mock)

	candidate := MetadataCandidate{
		Title:        "The Book of Dragons: An Anthology",
		Subtitle:     "An Anthology",
		Author:       "Garth Nix",
		Year:         2020,
		Genre:        "Fiction",
		PageCount:    560,
		ISBN10:       "0062877151",
		ISBN13:       "9780062877154",
		Source:       "Google Books",
		Score:        1,
		CategoryTags: []string{"Fiction", "Fiction / Fantasy / Collections & Anthologies"},
	}
	if _, err := svc.ApplyMetadataCandidate(bookID, candidate, nil); err != nil {
		t.Fatalf("ApplyMetadataCandidate: %v", err)
	}
	if saved == nil {
		t.Fatal("book was not saved")
	}
	if saved.Genre == nil || *saved.Genre != "Fiction" {
		t.Errorf("Genre = %v, want Fiction", saved.Genre)
	}
	if saved.PageCount == nil || *saved.PageCount != 560 {
		t.Errorf("PageCount = %v, want 560", saved.PageCount)
	}
	if saved.Subtitle == nil || *saved.Subtitle != "An Anthology" {
		t.Errorf("Subtitle = %v, want An Anthology", saved.Subtitle)
	}
	if saved.ISBN10 == nil || *saved.ISBN10 != "0062877151" || saved.ISBN13 == nil || *saved.ISBN13 != "9780062877154" {
		t.Errorf("ISBN10/13 = %v/%v", saved.ISBN10, saved.ISBN13)
	}
	if saved.PrintYear == nil || *saved.PrintYear != 2020 {
		t.Errorf("PrintYear = %v, want 2020 (Google's year is a print year)", saved.PrintYear)
	}
	if saved.AudiobookReleaseYear == nil || *saved.AudiobookReleaseYear != 2019 {
		t.Errorf("AudiobookReleaseYear = %v, want the untouched 2019", saved.AudiobookReleaseYear)
	}
	for _, tag := range candidate.CategoryTags {
		if tags[tag] != "google_books_category" {
			t.Errorf("tag %q source = %q, want google_books_category", tag, tags[tag])
		}
	}
}
