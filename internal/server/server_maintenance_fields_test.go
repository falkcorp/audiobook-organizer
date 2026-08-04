// file: internal/server/server_maintenance_fields_test.go
// version: 1.0.0
// guid: 7c4e1b58-2a93-4f60-9d17-5b8e03c2a7f4
// last-edited: 2026-08-03

package server

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// 🔴 TestApplyTranscriptionCandidate_AppliesOnlyGatedFields is the over-apply
// regression.
//
// ApplyTranscriptionCandidate used to call ApplyMetadataCandidate with a nil
// fields list, which means "no allowlist" — so the ENTIRE candidate was written:
// narrator, series, series_position, year, publisher, ISBN, description,
// language and cover_url.
//
// Nothing ever gated on those. The three gates in runAutoMatchTranscribed and
// both TOCTOU re-checks reason about exactly two fields, title and author. So a
// single passing title comparison authorised eight further unreviewed writes —
// in a repo that has already shipped write-back bugs that wiped Author/Series.
//
// This asserts the write is narrowed to what was actually checked.
func TestApplyTranscriptionCandidate_AppliesOnlyGatedFields(t *testing.T) {
	bookID := "book-fields"
	book := &database.Book{ID: bookID, Title: "Old Title"}

	// A candidate rich in ungated fields. Only Title and Author were gated.
	cand := metafetch.MetadataCandidate{
		Title:       "The Stable Book",
		Author:      "Stable Author",
		Narrator:    "Some Narrator Nobody Checked",
		Series:      "A Series Nobody Checked",
		Publisher:   "A Publisher Nobody Checked",
		ISBN:        "9780000000001",
		Description: "A description nobody checked.",
		Language:    "xx",
		Year:        1999,
		Score:       0.9,
		Source:      "test",
	}
	entry := mustCandidateCache(t, bookID, cand)

	store, updateCalls := newTOCTOUCacheStore(t, book, entry, entry)
	s := &Server{store: store, metadataFetchService: metafetch.NewService(store)}
	ctx := context.Background()

	candTitle, candAuthor, _, found, err := s.SearchTranscriptionCandidate(ctx, bookID, "irrelevant", "irrelevant")
	if err != nil || !found {
		t.Fatalf("SearchTranscriptionCandidate() = (found=%v, err=%v), want found=true", found, err)
	}
	if applyErr := s.ApplyTranscriptionCandidate(ctx, bookID, candTitle, candAuthor); applyErr != nil {
		t.Fatalf("ApplyTranscriptionCandidate() = %v, want nil", applyErr)
	}
	if len(*updateCalls) == 0 {
		t.Fatal("UpdateBook was never called — the gated fields must still apply")
	}

	got := (*updateCalls)[len(*updateCalls)-1]

	// The gated fields SHOULD land.
	if got.Title != cand.Title {
		t.Errorf("title = %q, want %q — the gated field must still be written", got.Title, cand.Title)
	}

	// Every ungated field must NOT land.
	if got.Publisher != nil && *got.Publisher != "" {
		t.Errorf("publisher = %q was written but nothing gated on it", *got.Publisher)
	}
	if got.ISBN13 != nil && *got.ISBN13 != "" {
		t.Errorf("isbn13 = %q was written but nothing gated on it", *got.ISBN13)
	}
	if got.ISBN10 != nil && *got.ISBN10 != "" {
		t.Errorf("isbn10 = %q was written but nothing gated on it", *got.ISBN10)
	}
	if got.Description != nil && *got.Description != "" {
		t.Errorf("description was written (%q) but nothing gated on it", *got.Description)
	}
	if got.Language != nil && *got.Language != "" {
		t.Errorf("language = %q was written but nothing gated on it", *got.Language)
	}
	if got.PrintYear != nil && *got.PrintYear != 0 {
		t.Errorf("print_year = %d was written but nothing gated on it", *got.PrintYear)
	}
	if got.AudiobookReleaseYear != nil && *got.AudiobookReleaseYear != 0 {
		t.Errorf("audiobook_release_year = %d was written but nothing gated on it", *got.AudiobookReleaseYear)
	}
	if got.Narrator != nil && *got.Narrator != "" {
		t.Errorf("narrator = %q was written but nothing gated on it", *got.Narrator)
	}
}
