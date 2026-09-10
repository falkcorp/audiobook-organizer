// file: internal/metafetch/service_apply_signal_fields_test.go
// version: 1.0.0
// guid: 8f2a6d13-4c9b-4e07-a1d5-6b3e9c2f70a8
// last-edited: 2026-09-10

package metafetch

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// TestApplyMetadataUnguarded_SignalFields locks the Phase 1 content-matcher
// signal fields end-to-end: every provider field the apply path now persists
// lands on the Book, the ISBN-10/13 split fills both columns, and a decimal
// series position is preserved as SeriesPositionRaw while the *int SeriesSequence
// is left unset (Atoi("1.5") fails) — proving the decimal is no longer lost.
func TestApplyMetadataUnguarded_SignalFields(t *testing.T) {
	mock := &database.MockStore{
		GetSeriesByNameFunc: func(name string, authorID *int) (*database.Series, error) {
			return &database.Series{ID: 7, Name: name}, nil
		},
	}
	svc := NewService(mock)

	abridged := true
	meta := metadata.BookMetadata{
		ISBN10:                  "1111111111",
		ISBN13:                  "9781111111111",
		ASIN:                    "B0TEST01",
		Abridged:                &abridged,
		Subtitle:                "The Subtitle",
		PageCount:               250,
		Series:                  "Main Series",
		SeriesPosition:          "1.5",
		SeriesSecondary:         "Side Series",
		SeriesSecondaryPosition: "2",
		DurationSec:             3600,
	}
	book := &database.Book{ID: "b1", Title: "Book"}
	svc.applyMetadataUnguarded(book, meta)

	assertStrPtr(t, "ISBN10", book.ISBN10, "1111111111")
	assertStrPtr(t, "ISBN13", book.ISBN13, "9781111111111")
	assertStrPtr(t, "ASIN", book.ASIN, "B0TEST01")
	assertStrPtr(t, "Subtitle", book.Subtitle, "The Subtitle")
	assertStrPtr(t, "SeriesPositionRaw", book.SeriesPositionRaw, "1.5")
	assertStrPtr(t, "SeriesSecondary", book.SeriesSecondary, "Side Series")
	assertStrPtr(t, "SeriesSecondaryPosition", book.SeriesSecondaryPosition, "2")

	if book.Abridged == nil || !*book.Abridged {
		t.Errorf("Abridged: got %v, want true", book.Abridged)
	}
	if book.PageCount == nil || *book.PageCount != 250 {
		t.Errorf("PageCount: got %v, want 250", book.PageCount)
	}
	if book.AudibleRuntimeMin == nil || *book.AudibleRuntimeMin != 60 {
		t.Errorf("AudibleRuntimeMin: got %v, want 60", book.AudibleRuntimeMin)
	}
	// The decimal must NOT be coerced into the *int served field.
	if book.SeriesSequence != nil {
		t.Errorf("SeriesSequence: got %v, want nil (decimal position must not be coerced)", *book.SeriesSequence)
	}
}

// TestApplyMetadataUnguarded_SingleISBNFallback covers the back-compat path: a
// provider that set only the single ISBN still fills the correct column by length.
func TestApplyMetadataUnguarded_SingleISBNFallback(t *testing.T) {
	svc := NewService(&database.MockStore{})

	book13 := &database.Book{ID: "b13", Title: "B"}
	svc.applyMetadataUnguarded(book13, metadata.BookMetadata{ISBN: "9782222222222"})
	assertStrPtr(t, "ISBN13 from single", book13.ISBN13, "9782222222222")
	if book13.ISBN10 != nil {
		t.Errorf("ISBN10 should be unset, got %v", *book13.ISBN10)
	}

	book10 := &database.Book{ID: "b10", Title: "B"}
	svc.applyMetadataUnguarded(book10, metadata.BookMetadata{ISBN: "2222222222"})
	assertStrPtr(t, "ISBN10 from single", book10.ISBN10, "2222222222")
	if book10.ISBN13 != nil {
		t.Errorf("ISBN13 should be unset, got %v", *book10.ISBN13)
	}
}

func assertStrPtr(t *testing.T, name string, got *string, want string) {
	t.Helper()
	if got == nil {
		t.Errorf("%s: got nil, want %q", name, want)
		return
	}
	if *got != want {
		t.Errorf("%s: got %q, want %q", name, *got, want)
	}
}
