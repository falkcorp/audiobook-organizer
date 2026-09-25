// file: internal/database/book_file_by_file_id_test.go
// version: 1.0.0
// guid: 2a004097-5d04-4e6d-9938-d6b6e0efe6a5
// last-edited: 2026-09-25

package database

import "testing"

func TestGetBookFileByFileID(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	book, err := store.CreateBook(&Book{Title: "owner", FilePath: "/lib/owner"})
	if err != nil {
		t.Fatal(err)
	}
	f := &BookFile{BookID: book.ID, FilePath: "/lib/owner/a.m4b"}
	if err := store.CreateBookFile(f); err != nil {
		t.Fatal(err)
	}

	r, ok := AsCapability[BookFileByFileIDReader](store)
	if !ok {
		t.Fatal("PebbleStore does not expose BookFileByFileIDReader")
	}
	got, err := r.GetBookFileByFileID(f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != f.ID || got.BookID != book.ID {
		t.Fatalf("got %+v, want file %s of book %s", got, f.ID, book.ID)
	}

	if got, err := r.GetBookFileByFileID("no-such-file"); err != nil || got != nil {
		t.Fatalf("unknown id: got (%+v, %v), want (nil, nil)", got, err)
	}

	if err := store.DeleteBookFile(f.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := r.GetBookFileByFileID(f.ID); err != nil || got != nil {
		t.Fatalf("deleted id: got (%+v, %v), want (nil, nil)", got, err)
	}
}
