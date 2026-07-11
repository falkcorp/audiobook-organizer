// file: internal/database/pebble_store_books_by_ids_test.go
// version: 1.0.0
// guid: 7e5d4c3b-2a19-4f8e-9d0c-1b2a3c4d5e6f
// last-edited: 2026-07-11

package database

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// newPebbleStoreForBooksByIDs mirrors newPebbleStoreForLSH's setup pattern
// (see pebble_store_lsh_test.go) for a fresh, isolated PebbleStore per test.
func newPebbleStoreForBooksByIDs(t *testing.T) *PebbleStore {
	t.Helper()
	store, err := NewPebbleStore(filepath.Join(t.TempDir(), "books-by-ids-db"))
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func mustCreateBookForBatch(t *testing.T, store *PebbleStore, id, title string) *Book {
	t.Helper()
	book := &Book{
		ID:       id,
		Title:    title,
		FilePath: "/tmp/" + id + "-" + title + ".mp3",
	}
	created, err := store.CreateBook(book)
	if err != nil {
		t.Fatalf("CreateBook %s: %v", id, err)
	}
	return created
}

// TestGetBooksByIDs_OrderPreserved asserts the batch getter returns rows in
// the same order as the requested ids, not storage/insertion order.
func TestGetBooksByIDs_OrderPreserved(t *testing.T) {
	store := newPebbleStoreForBooksByIDs(t)
	a := mustCreateBookForBatch(t, store, "book-a", "Alpha")
	b := mustCreateBookForBatch(t, store, "book-b", "Bravo")
	c := mustCreateBookForBatch(t, store, "book-c", "Charlie")

	got, err := store.GetBooksByIDs([]string{c.ID, a.ID, b.ID})
	if err != nil {
		t.Fatalf("GetBooksByIDs: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 books, got %d", len(got))
	}
	wantOrder := []string{c.ID, a.ID, b.ID}
	for i, id := range wantOrder {
		if got[i].ID != id {
			t.Errorf("index %d: got ID %q, want %q (order not preserved)", i, got[i].ID, id)
		}
	}
}

// TestGetBooksByIDs_UnknownIDSkipped asserts an ID that does not resolve is
// silently skipped, matching GetBookByID's nil-on-not-found semantics —
// no error, and the result simply omits that row.
func TestGetBooksByIDs_UnknownIDSkipped(t *testing.T) {
	store := newPebbleStoreForBooksByIDs(t)
	a := mustCreateBookForBatch(t, store, "book-a", "Alpha")

	got, err := store.GetBooksByIDs([]string{a.ID, "does-not-exist"})
	if err != nil {
		t.Fatalf("GetBooksByIDs: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 book (unknown ID skipped), got %d", len(got))
	}
	if got[0].ID != a.ID {
		t.Errorf("got ID %q, want %q", got[0].ID, a.ID)
	}
}

// TestGetBooksByIDs_EmptyInput asserts empty ids returns a non-nil empty
// slice (never nil) and a nil error.
func TestGetBooksByIDs_EmptyInput(t *testing.T) {
	store := newPebbleStoreForBooksByIDs(t)

	got, err := store.GetBooksByIDs([]string{})
	if err != nil {
		t.Fatalf("GetBooksByIDs: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil empty slice, got nil")
	}
	if len(got) != 0 {
		t.Fatalf("expected empty slice, got %d entries", len(got))
	}
}

// TestGetBooksByIDs_Fidelity asserts the batch getter is full-fidelity — it
// reuses GetBookByID's exact book:<id> point-get + json.Unmarshal, so heavy
// fields (Description, VersionNotes, BookSigV1 family) survive the round
// trip rather than being dropped by a memdb-slim projection.
func TestGetBooksByIDs_Fidelity(t *testing.T) {
	store := newPebbleStoreForBooksByIDs(t)

	desc := "a long description with plot details"
	versionNotes := "remastered edition notes"
	bookSigV1 := "b64-signature-payload"
	bookSigMask := "b64-coverage-mask"
	bookSigSegments := 4096
	bookSigCoveragePct := 87
	bookSigBuiltAt := time.Now().UTC().Truncate(time.Second)

	book := &Book{
		ID:                 "book-heavy",
		Title:              "Heavy Fields",
		FilePath:           "/tmp/book-heavy.mp3",
		Description:        &desc,
		VersionNotes:       &versionNotes,
		BookSigV1:          &bookSigV1,
		BookSigV1Mask:      &bookSigMask,
		BookSigSegments:    &bookSigSegments,
		BookSigCoveragePct: &bookSigCoveragePct,
		BookSigBuiltAt:     &bookSigBuiltAt,
	}
	created, err := store.CreateBook(book)
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}

	got, err := store.GetBooksByIDs([]string{created.ID})
	if err != nil {
		t.Fatalf("GetBooksByIDs: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 book, got %d", len(got))
	}
	row := got[0]
	if row.Description == nil || *row.Description != desc {
		t.Errorf("Description not full-fidelity: got %v, want %q", row.Description, desc)
	}
	if row.VersionNotes == nil || *row.VersionNotes != versionNotes {
		t.Errorf("VersionNotes not full-fidelity: got %v, want %q", row.VersionNotes, versionNotes)
	}
	if row.BookSigV1 == nil || *row.BookSigV1 != bookSigV1 {
		t.Errorf("BookSigV1 not full-fidelity: got %v, want %q", row.BookSigV1, bookSigV1)
	}
	if row.BookSigV1Mask == nil || *row.BookSigV1Mask != bookSigMask {
		t.Errorf("BookSigV1Mask not full-fidelity: got %v, want %q", row.BookSigV1Mask, bookSigMask)
	}
	if row.BookSigSegments == nil || *row.BookSigSegments != bookSigSegments {
		t.Errorf("BookSigSegments not full-fidelity: got %v, want %d", row.BookSigSegments, bookSigSegments)
	}
	if row.BookSigCoveragePct == nil || *row.BookSigCoveragePct != bookSigCoveragePct {
		t.Errorf("BookSigCoveragePct not full-fidelity: got %v, want %d", row.BookSigCoveragePct, bookSigCoveragePct)
	}
	if row.BookSigBuiltAt == nil || !row.BookSigBuiltAt.Equal(bookSigBuiltAt) {
		t.Errorf("BookSigBuiltAt not full-fidelity: got %v, want %v", row.BookSigBuiltAt, bookSigBuiltAt)
	}
}

// TestGetBooksByIDs_ErrorAlongsideRows asserts the spec §C3 contract: on the
// first non-not-found error (here, a corrupt/unmarshalable row mid-batch),
// the getter returns the rows read so far ALONGSIDE the error — never a
// bare (nil, err) — so the caller can still serve a partial page.
func TestGetBooksByIDs_ErrorAlongsideRows(t *testing.T) {
	store := newPebbleStoreForBooksByIDs(t)
	a := mustCreateBookForBatch(t, store, "book-a", "Alpha")
	b := mustCreateBookForBatch(t, store, "book-b", "Bravo")

	// Directly write an unparsable value under a book:<id> key, bypassing
	// CreateBook, to simulate a corrupt row mid-batch.
	corruptID := "book-corrupt"
	if err := store.db.Set([]byte(fmt.Sprintf("book:%s", corruptID)), []byte("{not valid json"), nil); err != nil {
		t.Fatalf("seed corrupt row: %v", err)
	}

	got, err := store.GetBooksByIDs([]string{a.ID, corruptID, b.ID})
	if err == nil {
		t.Fatal("expected a non-nil error for the corrupt row")
	}
	if len(got) != 1 || got[0].ID != a.ID {
		t.Fatalf("expected rows-read-so-far [%s], got %+v", a.ID, got)
	}
}
