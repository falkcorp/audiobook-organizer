// file: internal/database/pebble_store_chapters_test.go
// version: 1.1.0
// guid: efc1583b-2f69-4546-980f-92bea6798fb2
// last-edited: 2026-09-22

package database

import (
	"reflect"
	"testing"
)

// odysseyChapters mirrors the real, verified ffprobe -show_chapters output
// for the committed fixture
// testdata/audio/librivox/odyssey_butler_librivox/odyssey_complete.m4b (see
// docs/specs/2026-07-29-abs-sync-api-design.md §5b): 12 embedded chapters,
// first starting at 0.000000, last ending at 21744.489070. These exact float
// values are the ground truth this persistence layer must round-trip without
// coercion or rounding.
func odysseyChapters() []Chapter {
	return []Chapter{
		{ID: 0, StartSec: 0.000000, EndSec: 1386.002063, Title: "The Odyssey: Book 01"},
		{ID: 1, StartSec: 1386.002063, EndSec: 2788.017279, Title: "The Odyssey: Book 02"},
		{ID: 2, StartSec: 2788.017279, EndSec: 4309.006735, Title: "The Odyssey: Book 03"},
		{ID: 3, StartSec: 4309.006735, EndSec: 6929.004104, Title: "The Odyssey: Book 04"},
		{ID: 4, StartSec: 6929.004104, EndSec: 8602.009841, Title: "The Odyssey: Book 05"},
		{ID: 5, StartSec: 8602.009841, EndSec: 9975.017642, Title: "The Odyssey: Book 06"},
		{ID: 6, StartSec: 9975.017642, EndSec: 11125.007959, Title: "The Odyssey: Book 07"},
		{ID: 7, StartSec: 11125.007959, EndSec: 12938.005624, Title: "The Odyssey: Book 08"},
		{ID: 8, StartSec: 12938.005624, EndSec: 15242.004036, Title: "The Odyssey: Book 09"},
		{ID: 9, StartSec: 15242.004036, EndSec: 17466.005760, Title: "The Odyssey: Book 10"},
		{ID: 10, StartSec: 17466.005760, EndSec: 19940.007710, Title: "The Odyssey: Book 11"},
		{ID: 11, StartSec: 19940.007710, EndSec: 21744.489070, Title: "The Odyssey: Book 12"},
	}
}

// TestGetChaptersForBook_Absent_ReturnsNilNil verifies that a book ID which
// never had chapters saved returns (nil, nil), not an error -- callers must be
// able to distinguish "no chapters yet" from a store failure without
// inspecting error text.
func TestGetChaptersForBook_Absent_ReturnsNilNil(t *testing.T) {
	store, err := NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer store.Close()

	chs, err := store.GetChaptersForBook("book-that-never-existed")
	if err != nil {
		t.Fatalf("GetChaptersForBook() error = %v, want nil", err)
	}
	if chs != nil {
		t.Fatalf("GetChaptersForBook() = %v, want nil", chs)
	}
}

// TestSaveAndGetChaptersForBook_RoundTrip saves the real Odyssey fixture's 6
// chapters and reads them back, asserting exact equality including order --
// SaveChaptersForBook must not re-sort or coerce the float seconds on write,
// and GetChaptersForBook must not re-sort on read.
func TestSaveAndGetChaptersForBook_RoundTrip(t *testing.T) {
	store, err := NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer store.Close()

	const bookID = "book-odyssey"
	want := odysseyChapters()

	if err := store.SaveChaptersForBook(bookID, want); err != nil {
		t.Fatalf("SaveChaptersForBook() error = %v", err)
	}

	got, err := store.GetChaptersForBook(bookID)
	if err != nil {
		t.Fatalf("GetChaptersForBook() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GetChaptersForBook() = %+v, want %+v", got, want)
	}
}

// TestSaveChaptersForBook_EmptySlice_DeletesExistingEntry verifies that
// saving an empty/nil chapter list is equivalent to deleting the entry, not
// storing an empty JSON array blob -- a subsequent Get must return (nil, nil).
func TestSaveChaptersForBook_EmptySlice_DeletesExistingEntry(t *testing.T) {
	store, err := NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer store.Close()

	const bookID = "book-odyssey"
	if err := store.SaveChaptersForBook(bookID, odysseyChapters()); err != nil {
		t.Fatalf("SaveChaptersForBook(non-empty) error = %v", err)
	}

	if err := store.SaveChaptersForBook(bookID, nil); err != nil {
		t.Fatalf("SaveChaptersForBook(nil) error = %v", err)
	}

	got, err := store.GetChaptersForBook(bookID)
	if err != nil {
		t.Fatalf("GetChaptersForBook() error = %v", err)
	}
	if got != nil {
		t.Fatalf("GetChaptersForBook() after empty save = %v, want nil", got)
	}

	// Also verify the []Chapter{} (non-nil, zero-length) form behaves the same.
	if err := store.SaveChaptersForBook(bookID, odysseyChapters()); err != nil {
		t.Fatalf("SaveChaptersForBook(non-empty) error = %v", err)
	}
	if err := store.SaveChaptersForBook(bookID, []Chapter{}); err != nil {
		t.Fatalf("SaveChaptersForBook([]Chapter{}) error = %v", err)
	}
	got, err = store.GetChaptersForBook(bookID)
	if err != nil {
		t.Fatalf("GetChaptersForBook() error = %v", err)
	}
	if got != nil {
		t.Fatalf("GetChaptersForBook() after []Chapter{} save = %v, want nil", got)
	}
}

// TestDeleteChaptersForBook_Idempotent verifies that deleting chapters for a
// book that never had any is not an error (matches Pebble delete-absent-key
// semantics, mirroring DeleteMetadataCache).
func TestDeleteChaptersForBook_Idempotent(t *testing.T) {
	store, err := NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer store.Close()

	if err := store.DeleteChaptersForBook("book-that-never-had-chapters"); err != nil {
		t.Fatalf("DeleteChaptersForBook() error = %v, want nil", err)
	}
	// Calling it a second time must also be a no-op, not an error.
	if err := store.DeleteChaptersForBook("book-that-never-had-chapters"); err != nil {
		t.Fatalf("DeleteChaptersForBook() second call error = %v, want nil", err)
	}
}

// TestDeleteBook_CascadesChapters verifies that PebbleStore.DeleteBook tears
// down the book's persisted chapter list, so chapters never outlive their
// book as orphaned, unreadable Pebble rows.
func TestDeleteBook_CascadesChapters(t *testing.T) {
	store, err := NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer store.Close()

	created, err := store.CreateBook(&Book{Title: "Odyssey", FilePath: "/lib/odyssey.m4b"})
	if err != nil {
		t.Fatalf("CreateBook() error = %v", err)
	}

	if err := store.SaveChaptersForBook(created.ID, odysseyChapters()); err != nil {
		t.Fatalf("SaveChaptersForBook() error = %v", err)
	}

	if err := store.DeleteBook(created.ID); err != nil {
		t.Fatalf("DeleteBook() error = %v", err)
	}

	got, err := store.GetChaptersForBook(created.ID)
	if err != nil {
		t.Fatalf("GetChaptersForBook() error = %v", err)
	}
	if got != nil {
		t.Fatalf("GetChaptersForBook() after DeleteBook = %v, want nil", got)
	}
}
