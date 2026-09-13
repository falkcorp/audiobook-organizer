// file: internal/server/handlers/versions_split_one_book_pebble_test.go
// version: 1.0.0
// guid: 2b4d6f8a-0c1e-4f3a-8d5b-9e1f3a5c7b86
// last-edited: 2026-09-13

package handlers_test

import (
	"net/http"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
)

// End to end on the real Pebble store: split two of three files into one new
// book, then read both books back. Each row is under exactly one book, and
// both books' duration and size are the sums of the rows they now hold.
func TestSplitSegmentsToBooks_AsOneBook_PebbleStore(t *testing.T) {
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "split-db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	dur, size := 600, int64(60)
	src, err := store.CreateBook(&database.Book{Title: "Omnibus", FilePath: "/lib/Omnibus", Format: "mp3", Duration: &dur, FileSize: &size})
	if err != nil {
		t.Fatalf("create book: %v", err)
	}
	mk := func(path string, d int, s int64) string {
		f := &database.BookFile{BookID: src.ID, FilePath: path, Format: "mp3", Duration: d, FileSize: s}
		if err := store.CreateBookFile(f); err != nil {
			t.Fatalf("create file: %v", err)
		}
		return f.ID
	}
	f1 := mk("/lib/Omnibus/Book 1/01.mp3", 100, 10)
	f2 := mk("/lib/Omnibus/Book 1/02.mp3", 200, 20)
	f3 := mk("/lib/Omnibus/Book 2/01.mp3", 300, 30)

	c, w := splitReq(`{"segment_ids":["` + f1 + `","` + f2 + `"],"as_one_book":true,"title":"Book One"}`)
	c.Params[0].Value = src.ID
	handlers.NewVersionsHandler(store).SplitSegmentsToBooks(c)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}

	srcFiles, err := store.GetBookFiles(src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(srcFiles) != 1 || srcFiles[0].ID != f3 {
		t.Fatalf("source must keep only %s, has %+v", f3, srcFiles)
	}

	// Find the new book through the moved row: its BookID is the new book.
	moved, err := store.GetBookFileByPath("/lib/Omnibus/Book 1/01.mp3")
	if err != nil || moved == nil {
		t.Fatalf("moved row not found by path: %v", err)
	}
	newID := moved.BookID
	if newID == src.ID {
		t.Fatal("moved row still names the source book")
	}
	newFiles, err := store.GetBookFiles(newID)
	if err != nil {
		t.Fatal(err)
	}
	if len(newFiles) != 2 {
		t.Fatalf("new book must hold 2 rows, has %d", len(newFiles))
	}
	for _, f := range newFiles {
		if f.ID == f3 {
			t.Fatal("unselected row moved")
		}
		if still, _ := store.GetBookFileByID(src.ID, f.ID); still != nil {
			t.Fatalf("row %s is attached to both books", f.ID)
		}
	}

	newBook, err := store.GetBookByID(newID)
	if err != nil || newBook == nil {
		t.Fatalf("new book: %v", err)
	}
	if newBook.Title != "Book One" || newBook.VersionGroupID != nil {
		t.Fatalf("new book must be standalone and titled: %+v", newBook)
	}
	if newBook.FilePath != "/lib/Omnibus/Book 1" {
		t.Fatalf("new book path %q", newBook.FilePath)
	}
	if newBook.Duration == nil || *newBook.Duration != 300 || newBook.FileSize == nil || *newBook.FileSize != 30 {
		t.Fatalf("new book aggregates not recomputed: duration=%v size=%v", newBook.Duration, newBook.FileSize)
	}
	srcBook, err := store.GetBookByID(src.ID)
	if err != nil || srcBook == nil {
		t.Fatalf("source book: %v", err)
	}
	if srcBook.Duration == nil || *srcBook.Duration != 300 || srcBook.FileSize == nil || *srcBook.FileSize != 30 {
		t.Fatalf("source aggregates not recomputed: duration=%v size=%v", srcBook.Duration, srcBook.FileSize)
	}
	if srcBook.FilePath != "/lib/Omnibus/Book 2/01.mp3" {
		t.Fatalf("source path %q", srcBook.FilePath)
	}
}
