// file: internal/database/book_delete_owns_files_test.go
// version: 1.0.0
// guid: ea8c9be0-11d7-4035-b1ea-a50f919e06cd
// last-edited: 2026-09-19

package database

import (
	"errors"
	"testing"
)

// DeleteBook never deletes book_file rows, so deleting a book that owns any
// would orphan them. It must refuse (ErrBookOwnsFiles) and leave the book and
// its rows exactly as they were; once the rows have been moved to another
// book the now-empty shell deletes normally.
func TestDeleteBook_RefusesBookOwningFiles(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	src, err := store.CreateBook(&Book{Title: "Owner", FilePath: "/lib/own/src"})
	if err != nil {
		t.Fatal(err)
	}
	dst, err := store.CreateBook(&Book{Title: "Target", FilePath: "/lib/own/dst"})
	if err != nil {
		t.Fatal(err)
	}
	f := &BookFile{BookID: src.ID, FilePath: "/lib/own/src/01.m4b", FileSize: 1}
	if err := store.CreateBookFile(f); err != nil {
		t.Fatal(err)
	}

	err = store.DeleteBook(src.ID)
	if !errors.Is(err, ErrBookOwnsFiles) {
		t.Fatalf("DeleteBook = %v, want ErrBookOwnsFiles", err)
	}
	if b, _ := store.GetBookByID(src.ID); b == nil {
		t.Fatal("book was deleted despite the refusal")
	}
	if rows, _ := store.GetBookFiles(src.ID); len(rows) != 1 {
		t.Fatalf("rows after refusal = %d, want 1", len(rows))
	}

	if err := store.MoveBookFilesToBook([]string{f.ID}, src.ID, dst.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteBook(src.ID); err != nil {
		t.Fatalf("DeleteBook of the emptied shell: %v", err)
	}
	if b, _ := store.GetBookByID(src.ID); b != nil {
		t.Error("emptied shell survived DeleteBook")
	}
	if rows, _ := store.GetBookFiles(dst.ID); len(rows) != 1 {
		t.Errorf("target rows = %d, want the 1 moved row", len(rows))
	}
}
