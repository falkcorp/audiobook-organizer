// file: internal/database/pebble_store_chapters_test.go
// version: 1.0.0
// guid: efc1583b-2f69-4546-980f-92bea6798fb2
// last-edited: 2026-07-30

package database

import (
	"reflect"
	"testing"
)

// odysseyChapters mirrors the real, verified ffprobe -show_chapters output
// for the committed fixture
// testdata/audio/librivox/odyssey_butler_librivox/odyssey_complete.m4b (see
// docs/specs/2026-07-29-abs-sync-api-design.md §5b): 6 embedded chapters,
// first starting at 0.000000, last ending at 9975.428000. These exact float
// values are the ground truth this persistence layer must round-trip without
// coercion or rounding.
func odysseyChapters() []Chapter {
	return []Chapter{
		{ID: 0, StartSec: 0.000000, EndSec: 1386.057000, Title: "Chapter 1: odyssey_01_homer_butler_64kb"},
		{ID: 1, StartSec: 1386.057000, EndSec: 2788.701000, Title: "Chapter 2: odyssey_02_homer_butler_64kb"},
		{ID: 2, StartSec: 2788.701000, EndSec: 4309.210000, Title: "Chapter 3: odyssey_03_homer_butler_64kb"},
		{ID: 3, StartSec: 4309.210000, EndSec: 6928.977000, Title: "Chapter 4: odyssey_04_homer_butler_64kb"},
		{ID: 4, StartSec: 6928.977000, EndSec: 8602.198000, Title: "Chapter 5: odyssey_05_homer_butler_64kb"},
		{ID: 5, StartSec: 8602.198000, EndSec: 9975.428000, Title: "Chapter 6: odyssey_06_homer_butler_64kb"},
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
