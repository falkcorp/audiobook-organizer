// file: internal/database/pebble_store_isbn_index_test.go
// version: 1.0.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-06-14

// Tests for the book:isbn10:, book:isbn13:, and book:asin: secondary indexes.
// Covers: create → index present, update ISBN → old row gone/new row present,
// delete book → index gone, multi-book same-ISBN union, backfill helper.

package database

import (
	"path/filepath"
	"testing"
)

// newPebbleStoreForISBN opens a fresh PebbleStore in a temp directory and
// registers cleanup.  Returns a *PebbleStore so tests can call isbn-specific
// methods directly.
func newPebbleStoreForISBN(t *testing.T) *PebbleStore {
	t.Helper()
	store, err := NewPebbleStore(filepath.Join(t.TempDir(), "isbn-db"))
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// ptr returns a pointer to s — tiny helper for *string fields.
func ptr(s string) *string { return &s }

// createTestBookWithISBN inserts a minimal Book with the given ISBNs and
// returns its ID.
func createTestBookWithISBN(t *testing.T, s *PebbleStore, title, isbn10, isbn13, asin string) string {
	t.Helper()
	b := &Book{
		Title:    title,
		FilePath: "/tmp/" + title + ".m4b",
	}
	if isbn10 != "" {
		b.ISBN10 = ptr(isbn10)
	}
	if isbn13 != "" {
		b.ISBN13 = ptr(isbn13)
	}
	if asin != "" {
		b.ASIN = ptr(asin)
	}
	created, err := s.CreateBook(b)
	if err != nil {
		t.Fatalf("CreateBook %q: %v", title, err)
	}
	return created.ID
}

// TestISBNIndex_CreateWritesRows verifies that CreateBook writes the expected
// index rows for ISBN10, ISBN13, and ASIN.
func TestISBNIndex_CreateWritesRows(t *testing.T) {
	s := newPebbleStoreForISBN(t)

	id := createTestBookWithISBN(t, s, "book-a", "0123456789", "9780123456786", "B001234567")

	ids, err := s.GetBookIDsByISBNASIN("0123456789", "9780123456786", "B001234567")
	if err != nil {
		t.Fatalf("GetBookIDsByISBNASIN: %v", err)
	}
	if !containsID(ids, id) {
		t.Errorf("expected %q in index, got %v", id, ids)
	}
}

// TestISBNIndex_TwoBooksShareISBN13 verifies that the union lookup returns
// both books when they share an ISBN13 value.
func TestISBNIndex_TwoBooksShareISBN13(t *testing.T) {
	s := newPebbleStoreForISBN(t)

	idA := createTestBookWithISBN(t, s, "book-a", "", "9780123456786", "")
	idB := createTestBookWithISBN(t, s, "book-b", "", "9780123456786", "")

	ids, err := s.GetBookIDsByISBNASIN("", "9780123456786", "")
	if err != nil {
		t.Fatalf("GetBookIDsByISBNASIN: %v", err)
	}
	if !containsID(ids, idA) {
		t.Errorf("expected idA=%q in index, got %v", idA, ids)
	}
	if !containsID(ids, idB) {
		t.Errorf("expected idB=%q in index, got %v", idB, ids)
	}
}

// TestISBNIndex_UpdateChangesISBN verifies that UpdateBook deletes the old
// ISBN index row and writes the new one.
func TestISBNIndex_UpdateChangesISBN(t *testing.T) {
	s := newPebbleStoreForISBN(t)

	id := createTestBookWithISBN(t, s, "book-a", "", "9780000000001", "")

	// Verify initial index row is present.
	ids, err := s.GetBookIDsByISBNASIN("", "9780000000001", "")
	if err != nil {
		t.Fatalf("initial GetBookIDsByISBNASIN: %v", err)
	}
	if !containsID(ids, id) {
		t.Errorf("expected id in initial index")
	}

	// Update the book to change ISBN13.
	updated := &Book{
		Title:    "book-a",
		FilePath: "/tmp/book-a.m4b",
		ISBN13:   ptr("9780000000002"),
	}
	if _, err := s.UpdateBook(id, updated); err != nil {
		t.Fatalf("UpdateBook: %v", err)
	}

	// Old ISBN13 must be gone.
	oldIDs, err := s.GetBookIDsByISBNASIN("", "9780000000001", "")
	if err != nil {
		t.Fatalf("old GetBookIDsByISBNASIN: %v", err)
	}
	if containsID(oldIDs, id) {
		t.Errorf("old ISBN13 index row still present after update")
	}

	// New ISBN13 must be present.
	newIDs, err := s.GetBookIDsByISBNASIN("", "9780000000002", "")
	if err != nil {
		t.Fatalf("new GetBookIDsByISBNASIN: %v", err)
	}
	if !containsID(newIDs, id) {
		t.Errorf("expected id in new ISBN13 index, got %v", newIDs)
	}
}

// TestISBNIndex_DeleteClearsRows verifies that DeleteBook removes all ISBN
// index rows for the book.
func TestISBNIndex_DeleteClearsRows(t *testing.T) {
	s := newPebbleStoreForISBN(t)

	id := createTestBookWithISBN(t, s, "book-a", "0123456789", "9780123456786", "B001234567")

	// Confirm rows exist.
	ids, err := s.GetBookIDsByISBNASIN("0123456789", "", "")
	if err != nil {
		t.Fatalf("pre-delete GetBookIDsByISBNASIN: %v", err)
	}
	if !containsID(ids, id) {
		t.Fatalf("expected id before delete")
	}

	// Delete the book.
	if err := s.DeleteBook(id); err != nil {
		t.Fatalf("DeleteBook: %v", err)
	}

	// All three namespaces must be empty.
	for _, tc := range []struct {
		isbn10, isbn13, asin string
	}{
		{"0123456789", "", ""},
		{"", "9780123456786", ""},
		{"", "", "B001234567"},
	} {
		ids, err := s.GetBookIDsByISBNASIN(tc.isbn10, tc.isbn13, tc.asin)
		if err != nil {
			t.Fatalf("GetBookIDsByISBNASIN after delete: %v", err)
		}
		if containsID(ids, id) {
			t.Errorf("stale index row after delete: isbn10=%q isbn13=%q asin=%q, got %v",
				tc.isbn10, tc.isbn13, tc.asin, ids)
		}
	}
}

// TestISBNIndex_EmptyValues verifies that empty strings are not indexed.
func TestISBNIndex_EmptyValues(t *testing.T) {
	s := newPebbleStoreForISBN(t)

	// Book with no ISBN at all.
	_ = createTestBookWithISBN(t, s, "book-noISBN", "", "", "")

	// Querying with all empty args should return nothing (and not panic).
	ids, err := s.GetBookIDsByISBNASIN("", "", "")
	if err != nil {
		t.Fatalf("GetBookIDsByISBNASIN empty: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("expected no results for empty query, got %v", ids)
	}
}

// TestISBNIndex_WriteISBNIndexForBook verifies the backfill helper.
func TestISBNIndex_WriteISBNIndexForBook(t *testing.T) {
	s := newPebbleStoreForISBN(t)

	// Write index rows directly (simulating backfill for a pre-existing book).
	const syntheticID = "01BACKFILLTEST00000000000"
	if err := s.WriteISBNIndexForBook(syntheticID, "0987654321", "9780987654321", "B009876543"); err != nil {
		t.Fatalf("WriteISBNIndexForBook: %v", err)
	}

	ids, err := s.GetBookIDsByISBNASIN("0987654321", "9780987654321", "B009876543")
	if err != nil {
		t.Fatalf("GetBookIDsByISBNASIN after backfill write: %v", err)
	}
	if !containsID(ids, syntheticID) {
		t.Errorf("expected syntheticID in backfill index, got %v", ids)
	}
}

// TestISBNIndex_IsBuiltFlag verifies the IsISBNIndexBuilt / SetISBNIndexBuilt flag.
func TestISBNIndex_IsBuiltFlag(t *testing.T) {
	s := newPebbleStoreForISBN(t)

	if s.IsISBNIndexBuilt() {
		t.Error("expected IsISBNIndexBuilt=false on fresh store")
	}

	if err := s.SetISBNIndexBuilt(); err != nil {
		t.Fatalf("SetISBNIndexBuilt: %v", err)
	}

	if !s.IsISBNIndexBuilt() {
		t.Error("expected IsISBNIndexBuilt=true after SetISBNIndexBuilt")
	}
}

// containsID is a helper that reports whether target appears in ids.
func containsID(ids []string, target string) bool {
	for _, id := range ids {
		if id == target {
			return true
		}
	}
	return false
}
