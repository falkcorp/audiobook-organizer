// file: internal/database/pebble_book_preserve_test.go
// version: 1.2.0
// guid: 9a2f1c6e-4d3b-4a71-8e2c-6b7d8f9012ab
// last-edited: 2026-10-15

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
	s, err := NewPebbleStoreInMemory("db")
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
	s, err := NewPebbleStoreInMemory("db")
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

// UpdateBook must NOT wipe the denormalized Author/Series when the incoming
// row leaves them nil (STOREFID W5d-1 / #1887). Author/Series carry
// json:",omitempty" and Pebble persists them (db:"-" only suppresses SQLite),
// so a BookCore->ToBook or memdb-projection write-back with them nil erased
// them from the stored Pebble row before this guard existed. On UpdateBook a
// nil there is a projection, not a clear, so preserve-on-nil is correct — the
// same class of fix as the seven memdb-stripped fields above. ModifyBook is
// the other way round: TestModifyBook_NilAuthorIsWrittenAsNil.
func TestUpdateBook_PreservesAuthorSeriesOnNilIncoming(t *testing.T) {
	s, err := NewPebbleStoreInMemory("db")
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer s.Close()

	book, err := s.CreateBook(&Book{Title: "Denorm Book"})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}

	authorID := 42
	seriesID := 7
	author := &Author{ID: authorID, Name: "Ursula K. Le Guin"}
	series := &Series{ID: seriesID, Name: "Earthsea"}

	// Seed the denormalized Author/Series (plus the identity IDs) via a full
	// write, as a hydrated caller would.
	if _, err := s.UpdateBook(book.ID, &Book{
		ID:       book.ID,
		Title:    book.Title,
		AuthorID: &authorID,
		SeriesID: &seriesID,
		Author:   author,
		Series:   series,
	}); err != nil {
		t.Fatalf("UpdateBook (seed): %v", err)
	}

	// Simulate a BookCore->ToBook / memdb-projection round trip: incoming
	// *Book has nil Author/Series (as ToBook leaves them) but changes an
	// unrelated field.
	newTitle := "Denorm Book Renamed"
	if _, err := s.UpdateBook(book.ID, &Book{
		ID:       book.ID,
		Title:    newTitle,
		AuthorID: &authorID,
		SeriesID: &seriesID,
	}); err != nil {
		t.Fatalf("UpdateBook (projection round trip): %v", err)
	}

	got, err := s.GetBookByID(book.ID) // pebble-direct → the stored row
	if err != nil || got == nil {
		t.Fatalf("GetBookByID: err=%v got=%v", err, got)
	}
	if got.Title != newTitle {
		t.Errorf("Title not updated: got %q, want %q", got.Title, newTitle)
	}
	if got.Author == nil {
		t.Fatal("Author WIPED: got nil, want preserved denormalized author")
	}
	if got.Author.ID != authorID || got.Author.Name != author.Name {
		t.Errorf("Author corrupted: got %+v, want %+v", got.Author, author)
	}
	if got.Series == nil {
		t.Fatal("Series WIPED: got nil, want preserved denormalized series")
	}
	if got.Series.ID != seriesID || got.Series.Name != series.Name {
		t.Errorf("Series corrupted: got %+v, want %+v", got.Series, series)
	}
}

// ModifyBook and ModifyBookCredits hand their callback the full stored row, so
// a nil Author the callback leaves is a deliberate clear and must be written as
// nil. Before bookWriteOpts.authorAsGiven the UpdateBook guard put the old
// object back: a revert that moved a primary back to an id with no author row
// left the book displaying the author it had just been moved off.
func TestModifyBook_NilAuthorIsWrittenAsNil(t *testing.T) {
	s, err := NewPebbleStoreInMemory("db")
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer s.Close()

	authorID := 42
	seed := func(title string) string {
		t.Helper()
		book, err := s.CreateBook(&Book{Title: title})
		if err != nil {
			t.Fatalf("CreateBook: %v", err)
		}
		if _, err := s.UpdateBook(book.ID, &Book{ID: book.ID, Title: title, AuthorID: &authorID,
			Author: &Author{ID: authorID, Name: "Synthetic Writer"}}); err != nil {
			t.Fatalf("UpdateBook (seed): %v", err)
		}
		return book.ID
	}
	stored := func(id string) *Book {
		t.Helper()
		got, err := s.GetBookByID(id)
		if err != nil || got == nil {
			t.Fatalf("GetBookByID(%s): err=%v got=%v", id, err, got)
		}
		return got
	}

	viaModify := seed("Modify Book")
	dangling := 424242
	if _, err := s.ModifyBook(viaModify, func(b *Book) error {
		b.AuthorID, b.Author = &dangling, nil
		return nil
	}); err != nil {
		t.Fatalf("ModifyBook: %v", err)
	}
	if got := stored(viaModify); got.Author != nil || got.AuthorID == nil || *got.AuthorID != dangling {
		t.Fatalf("ModifyBook clear: AuthorID=%v Author=%+v, want %d and nil", got.AuthorID, got.Author, dangling)
	}

	viaCredits := seed("Credits Book")
	if _, _, err := s.ModifyBookCredits(viaCredits, func(b *Book, _ *BookCreditsEdit) error {
		b.AuthorID, b.Author = nil, nil
		return nil
	}); err != nil {
		t.Fatalf("ModifyBookCredits: %v", err)
	}
	if got := stored(viaCredits); got.Author != nil || got.AuthorID != nil {
		t.Fatalf("ModifyBookCredits clear: AuthorID=%v Author=%+v, want both nil", got.AuthorID, got.Author)
	}

	// A callback that leaves Author alone keeps it: only an explicit nil clears.
	untouched := seed("Untouched Book")
	if _, err := s.ModifyBook(untouched, func(b *Book) error {
		b.Title = "Untouched Book Renamed"
		return nil
	}); err != nil {
		t.Fatalf("ModifyBook (title only): %v", err)
	}
	if got := stored(untouched); got.Author == nil || got.Author.ID != authorID {
		t.Fatalf("title-only ModifyBook lost Author: %+v", got.Author)
	}
}
