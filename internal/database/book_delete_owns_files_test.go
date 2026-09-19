// file: internal/database/book_delete_owns_files_test.go
// version: 1.1.0
// guid: ea8c9be0-11d7-4035-b1ea-a50f919e06cd
// last-edited: 2026-09-19

package database

import (
	"errors"
	"fmt"
	"sync"
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

// assertRowsHaveOwner fails for any book_file row of bookID when bookID has no
// book row. Reads Pebble (GetBookFiles), not memdb.
func assertRowsHaveOwner(t *testing.T, store Store, bookID string) {
	t.Helper()
	rows, err := store.GetBookFiles(bookID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		return
	}
	if b, _ := store.GetBookByID(bookID); b == nil {
		t.Fatalf("orphan: %d book_file row(s) name deleted book %s", len(rows), bookID)
	}
}

// A move INTO a book racing that book's DeleteBook must never leave the moved
// rows under a deleted book. Before the owner stripe, DeleteBook's count and
// the move's commit did not serialize: the count saw zero rows, the move
// committed, and the delete committed over it. Run with -race.
func TestDeleteBook_RacingMoveIntoBookNeverOrphans(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	const rounds = 60
	for i := range rounds {
		src, err := store.CreateBook(&Book{Title: "src", FilePath: fmt.Sprintf("/lib/race/src%d", i)})
		if err != nil {
			t.Fatal(err)
		}
		dst, err := store.CreateBook(&Book{Title: "dst", FilePath: fmt.Sprintf("/lib/race/dst%d", i)})
		if err != nil {
			t.Fatal(err)
		}
		f := &BookFile{BookID: src.ID, FilePath: fmt.Sprintf("/lib/race/src%d/01.m4b", i), FileSize: 1}
		if err := store.CreateBookFile(f); err != nil {
			t.Fatal(err)
		}

		var wg sync.WaitGroup
		var moveErr, delErr error
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			moveErr = store.MoveBookFilesToBook([]string{f.ID}, src.ID, dst.ID)
		}()
		go func() {
			defer wg.Done()
			<-start
			delErr = store.DeleteBook(dst.ID)
		}()
		close(start)
		wg.Wait()

		// Exactly one of the two orders happened, and each is fine:
		//   move first  -> delete refused (ErrBookOwnsFiles), dst keeps the row
		//   delete first -> move refused (ErrBookFileOwnerMissing), src keeps it
		switch {
		case moveErr == nil && delErr == nil:
			t.Fatalf("round %d: both the move and the delete succeeded", i)
		case moveErr != nil && !errors.Is(moveErr, ErrBookFileOwnerMissing):
			t.Fatalf("round %d: move: %v", i, moveErr)
		case delErr != nil && !errors.Is(delErr, ErrBookOwnsFiles):
			t.Fatalf("round %d: delete: %v", i, delErr)
		}
		assertRowsHaveOwner(t, store, dst.ID)
		assertRowsHaveOwner(t, store, src.ID)
	}
}

// Same race for a row CREATED under a book being deleted.
func TestDeleteBook_RacingCreateBookFileNeverOrphans(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	for i := range 60 {
		b, err := store.CreateBook(&Book{Title: "b", FilePath: fmt.Sprintf("/lib/racec/b%d", i)})
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		start := make(chan struct{})
		var createErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			createErr = store.CreateBookFile(&BookFile{BookID: b.ID, FilePath: fmt.Sprintf("/lib/racec/b%d/01.m4b", i)})
		}()
		go func() {
			defer wg.Done()
			<-start
			_ = store.DeleteBook(b.ID)
		}()
		close(start)
		wg.Wait()
		if createErr != nil && !errors.Is(createErr, ErrBookFileOwnerMissing) {
			t.Fatalf("round %d: create: %v", i, createErr)
		}
		assertRowsHaveOwner(t, store, b.ID)
	}
}

// A book_file row cannot be created for a book that has no row.
func TestCreateBookFile_RefusesMissingOwner(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	err := store.CreateBookFile(&BookFile{BookID: "no-such-book", FilePath: "/lib/x.m4b"})
	if !errors.Is(err, ErrBookFileOwnerMissing) {
		t.Fatalf("CreateBookFile = %v, want ErrBookFileOwnerMissing", err)
	}
	if rows, _ := store.GetBookFiles("no-such-book"); len(rows) != 0 {
		t.Fatalf("%d orphan row(s) written", len(rows))
	}
}

// BookFilesAtPath returns every row at a path, including one the
// single-valued book_file_path index no longer names.
func TestBookFilesAtPath_FindsRowsTheSingleIndexLost(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	ps := store.(*PebbleStore)
	ps.WaitForWarmup()

	a, _ := store.CreateBook(&Book{Title: "a", FilePath: "/lib/at/a"})
	b, _ := store.CreateBook(&Book{Title: "b", FilePath: "/lib/at/b"})
	keep := &BookFile{BookID: a.ID, FilePath: "/lib/at/x.m4b"}
	gone := &BookFile{BookID: b.ID, FilePath: "/lib/at/x.m4b"}
	if err := store.CreateBookFile(keep); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBookFile(gone); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteBookFile(gone.ID); err != nil {
		t.Fatal(err)
	}
	if hit, _ := store.GetBookFileByPath("/lib/at/x.m4b"); hit != nil {
		t.Fatalf("fixture is vacuous: single index still names %s", hit.ID)
	}
	rows, err := ps.BookFilesAtPath("/lib/at/x.m4b")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != keep.ID {
		t.Fatalf("rows = %+v, want exactly %s", rows, keep.ID)
	}
}
