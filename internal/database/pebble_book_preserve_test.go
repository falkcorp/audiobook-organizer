// file: internal/database/pebble_book_preserve_test.go
// version: 1.0.0
// guid: 9a2f1c6e-4d3b-4a71-8e2c-6b7d8f9012ab
// last-edited: 2026-07-03

package database

import (
	"testing"
	"time"
)

// UpdateBook must NOT wipe Description/VersionNotes/BookSig* when the
// incoming row leaves them nil (STOR-1). This is the memdb-round-trip
// footgun: callers source `book` from GetAllBooks on the production
// UseMemDB path (stripBookForMemdb nils these seven fields) and write it
// back via UpdateBook. Without the preserve-on-nil guard, that round trip
// silently erases the fields from the stored row. GetBookByID is
// pebble-direct so it reflects the actually-stored row, not the stripped
// memdb copy.
func TestUpdateBook_PreservesMemDBStrippedFieldsOnNilIncoming(t *testing.T) {
	s, err := NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer s.Close()

	book, err := s.CreateBook(&Book{Title: "Sig Book"})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}

	desc := "a great book"
	notes := "v2 remaster"
	sig := "base64sigdata"
	mask := "base64maskdata"
	segments := 4096
	builtAt := time.Now().Truncate(time.Second)
	coverage := 87

	// Set the seven fields via a direct UpdateBook call (simulating the
	// original, unstripped write path).
	if _, err := s.UpdateBook(book.ID, &Book{
		ID:                 book.ID,
		Title:              book.Title,
		Description:        &desc,
		VersionNotes:       &notes,
		BookSigV1:          &sig,
		BookSigV1Mask:      &mask,
		BookSigSegments:    &segments,
		BookSigBuiltAt:     &builtAt,
		BookSigCoveragePct: &coverage,
	}); err != nil {
		t.Fatalf("UpdateBook (seed): %v", err)
	}

	// Simulate a memdb-sourced round trip: incoming *Book has all seven
	// fields nil (as stripBookForMemdb would leave them) but changes an
	// unrelated field.
	newTitle := "Sig Book Renamed"
	if _, err := s.UpdateBook(book.ID, &Book{
		ID:    book.ID,
		Title: newTitle,
	}); err != nil {
		t.Fatalf("UpdateBook (memdb round trip): %v", err)
	}

	got, err := s.GetBookByID(book.ID) // pebble-direct → the stored row
	if err != nil || got == nil {
		t.Fatalf("GetBookByID: err=%v got=%v", err, got)
	}

	if got.Title != newTitle {
		t.Errorf("Title not updated: got %q, want %q", got.Title, newTitle)
	}
	if got.Description == nil || *got.Description != desc {
		t.Errorf("Description WIPED: got %v, want %q", got.Description, desc)
	}
	if got.VersionNotes == nil || *got.VersionNotes != notes {
		t.Errorf("VersionNotes WIPED: got %v, want %q", got.VersionNotes, notes)
	}
	if got.BookSigV1 == nil || *got.BookSigV1 != sig {
		t.Errorf("BookSigV1 WIPED: got %v, want %q", got.BookSigV1, sig)
	}
	if got.BookSigV1Mask == nil || *got.BookSigV1Mask != mask {
		t.Errorf("BookSigV1Mask WIPED: got %v, want %q", got.BookSigV1Mask, mask)
	}
	if got.BookSigSegments == nil || *got.BookSigSegments != segments {
		t.Errorf("BookSigSegments WIPED: got %v, want %d", got.BookSigSegments, segments)
	}
	if got.BookSigBuiltAt == nil || !got.BookSigBuiltAt.Equal(builtAt) {
		t.Errorf("BookSigBuiltAt WIPED: got %v, want %v", got.BookSigBuiltAt, builtAt)
	}
	if got.BookSigCoveragePct == nil || *got.BookSigCoveragePct != coverage {
		t.Errorf("BookSigCoveragePct WIPED: got %v, want %d", got.BookSigCoveragePct, coverage)
	}
}

// A genuine explicit clear (pointer-to-empty-string for Description) must
// still overwrite the stored value — the preserve guard only fires on nil,
// never on a non-nil "clear" value. This proves the escape hatch for
// user-initiated edits (internal/audiobooks/update_service.go) still works.
func TestUpdateBook_ExplicitClearStillOverwrites(t *testing.T) {
	s, err := NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer s.Close()

	book, err := s.CreateBook(&Book{Title: "Clear Book"})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}

	desc := "will be cleared"
	if _, err := s.UpdateBook(book.ID, &Book{
		ID:          book.ID,
		Title:       book.Title,
		Description: &desc,
	}); err != nil {
		t.Fatalf("UpdateBook (seed): %v", err)
	}

	empty := ""
	if _, err := s.UpdateBook(book.ID, &Book{
		ID:          book.ID,
		Title:       book.Title,
		Description: &empty,
	}); err != nil {
		t.Fatalf("UpdateBook (explicit clear): %v", err)
	}

	got, err := s.GetBookByID(book.ID)
	if err != nil || got == nil {
		t.Fatalf("GetBookByID: err=%v got=%v", err, got)
	}
	if got.Description == nil {
		t.Fatal("Description became nil — explicit clear should store pointer-to-empty-string, not nil")
	}
	if *got.Description != "" {
		t.Errorf("Description not cleared: got %q, want empty string", *got.Description)
	}
}
