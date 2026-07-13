// file: internal/server/entities_ops_hydrate_test.go
// version: 1.0.0
// guid: b9d41f27-8e6a-4c02-9f5b-2a7c14e630d8
// last-edited: 2026-07-13

package server

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// fullyPopulatedBook builds a Book with every field the resolve-production-author
// write path would otherwise wipe set to a non-zero value, so a regression can be
// detected as any of them reverting to its zero value.
func fullyPopulatedBook(t *testing.T, store *database.PebbleStore) *database.Book {
	t.Helper()
	sp := func(s string) *string { return &s }
	ip := func(i int) *int { return &i }
	fp := func(f float64) *float64 { return &f }

	authorID := 42
	seriesID := 7
	book := &database.Book{
		Title:                "The Well-Populated Book",
		AuthorID:             ip(authorID),
		SeriesID:             ip(seriesID),
		FilePath:             "/library/audiobooks/well-populated-book.m4b",
		Narrator:             sp("Jane Reader"),
		Genre:                sp("Science Fiction"),
		Publisher:            nil, // intentionally empty so the reclassify branch fires
		ISBN10:               sp("0123456789"),
		ISBN13:               sp("9780123456789"),
		ASIN:                 sp("B0000000AB"),
		Description:          sp("A long description that lives only in the full Pebble row."),
		ITunesRating:         ip(80),
		AudibleRatingOverall: fp(4.7),
		AudibleRatingCount:   ip(1234),
		GoogleRatingAverage:  fp(4.2),
	}
	created, err := store.CreateBook(book)
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	return created
}

// assertRecordIntact verifies every field that the full-replace UpdateBook wipe
// would have destroyed is still present on the stored row.
func assertRecordIntact(t *testing.T, store *database.PebbleStore, id string) *database.Book {
	t.Helper()
	got, err := store.GetBookByID(id)
	if err != nil {
		t.Fatalf("GetBookByID: %v", err)
	}
	if got == nil {
		t.Fatal("book vanished from store after write")
	}
	if got.Title != "The Well-Populated Book" {
		t.Errorf("Title wiped: got %q", got.Title)
	}
	if got.FilePath != "/library/audiobooks/well-populated-book.m4b" {
		t.Errorf("FilePath wiped: got %q", got.FilePath)
	}
	if got.SeriesID == nil || *got.SeriesID != 7 {
		t.Errorf("SeriesID wiped: got %v", got.SeriesID)
	}
	if got.Narrator == nil || *got.Narrator != "Jane Reader" {
		t.Errorf("Narrator wiped: got %v", got.Narrator)
	}
	if got.Genre == nil || *got.Genre != "Science Fiction" {
		t.Errorf("Genre wiped: got %v", got.Genre)
	}
	if got.ISBN13 == nil || *got.ISBN13 != "9780123456789" {
		t.Errorf("ISBN13 wiped: got %v", got.ISBN13)
	}
	if got.ASIN == nil || *got.ASIN != "B0000000AB" {
		t.Errorf("ASIN wiped: got %v", got.ASIN)
	}
	if got.Description == nil || *got.Description == "" {
		t.Errorf("Description wiped: got %v", got.Description)
	}
	if got.ITunesRating == nil || *got.ITunesRating != 80 {
		t.Errorf("ITunesRating wiped: got %v", got.ITunesRating)
	}
	if got.AudibleRatingOverall == nil || *got.AudibleRatingOverall != 4.7 {
		t.Errorf("AudibleRatingOverall wiped: got %v", got.AudibleRatingOverall)
	}
	if got.GoogleRatingAverage == nil || *got.GoogleRatingAverage != 4.2 {
		t.Errorf("GoogleRatingAverage wiped: got %v", got.GoogleRatingAverage)
	}
	return got
}

// TestAssignPublisherPreservingRecord proves the publisher-reclassify write path
// (resolve-production-author site #1) sets ONLY Publisher and leaves every other
// field — and the book:path: index — intact. This is the load-bearing regression
// for the full-record wipe: reverting the call site to a bare UpdateBook literal
// reintroduces the wipe inside the tested function and fails this test.
func TestAssignPublisherPreservingRecord(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	book := fullyPopulatedBook(t, store)

	if err := assignPublisherPreservingRecord(store, book.ID, "SomeProduction LLC"); err != nil {
		t.Fatalf("assignPublisherPreservingRecord: %v", err)
	}

	got := assertRecordIntact(t, store, book.ID)
	if got.Publisher == nil || *got.Publisher != "SomeProduction LLC" {
		t.Errorf("Publisher not set: got %v", got.Publisher)
	}
	// AuthorID must survive the publisher write (only Publisher was intended).
	if got.AuthorID == nil || *got.AuthorID != 42 {
		t.Errorf("AuthorID wiped by publisher write: got %v", got.AuthorID)
	}
	// The book:path: index must still resolve — the wipe would have blanked
	// FilePath and deleted/corrupted this index key.
	byPath, err := store.GetBookByFilePath(book.FilePath)
	if err != nil {
		t.Fatalf("GetBookByFilePath: %v", err)
	}
	if byPath == nil || byPath.ID != book.ID {
		t.Errorf("book:path: index corrupted: got %v", byPath)
	}
}

// TestAssignResolvedAuthorPreservingRecord proves the AI-cover author-resolve
// write path (resolve-production-author site #2) sets ONLY AuthorID (plus the
// book_authors join) and leaves every other field intact.
func TestAssignResolvedAuthorPreservingRecord(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	book := fullyPopulatedBook(t, store)
	const prodAuthorID = 42
	const resolvedAuthorID = 99

	if err := assignResolvedAuthorPreservingRecord(store, book.ID, resolvedAuthorID, prodAuthorID); err != nil {
		t.Fatalf("assignResolvedAuthorPreservingRecord: %v", err)
	}

	got := assertRecordIntact(t, store, book.ID)
	if got.AuthorID == nil || *got.AuthorID != resolvedAuthorID {
		t.Errorf("AuthorID not set to resolved author: got %v", got.AuthorID)
	}
	byPath, err := store.GetBookByFilePath(book.FilePath)
	if err != nil {
		t.Fatalf("GetBookByFilePath: %v", err)
	}
	if byPath == nil || byPath.ID != book.ID {
		t.Errorf("book:path: index corrupted: got %v", byPath)
	}
}

// TestAssignPreservingRecord_FailClosedOnMissing proves that when hydration finds
// no row, NOTHING is written — the helper returns an error and does not create or
// mutate any record (fail-closed).
func TestAssignPreservingRecord_FailClosedOnMissing(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if err := assignPublisherPreservingRecord(store, "does-not-exist", "X"); err == nil {
		t.Error("assignPublisherPreservingRecord: expected error on missing book, got nil")
	}
	if err := assignResolvedAuthorPreservingRecord(store, "does-not-exist", 1, 2); err == nil {
		t.Error("assignResolvedAuthorPreservingRecord: expected error on missing book, got nil")
	}
	// No phantom row should have been created by the failed writes.
	if got, _ := store.GetBookByID("does-not-exist"); got != nil {
		t.Errorf("fail-closed violated: a row was written for a missing book: %v", got)
	}
}
