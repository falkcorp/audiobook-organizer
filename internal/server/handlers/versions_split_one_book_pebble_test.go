// file: internal/server/handlers/versions_split_one_book_pebble_test.go
// version: 1.3.0
// guid: 2b4d6f8a-0c1e-4f3a-8d5b-9e1f3a5c7b86
// last-edited: 2026-09-13

package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
)

func openSplitStore(t *testing.T) *database.PebbleStore {
	t.Helper()
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "split-db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// seedSplitBook creates a book at bookPath holding one row per file path and
// returns the book and the row ids in order.
func seedSplitBook(t *testing.T, store *database.PebbleStore, bookPath string, files ...string) (*database.Book, []string) {
	t.Helper()
	src, err := store.CreateBook(&database.Book{Title: "Source", FilePath: bookPath, Format: "mp3"})
	if err != nil {
		t.Fatalf("create book: %v", err)
	}
	ids := make([]string, 0, len(files))
	for _, p := range files {
		f := &database.BookFile{BookID: src.ID, FilePath: p, Format: "mp3", Duration: 100, FileSize: 10}
		if err := store.CreateBookFile(f); err != nil {
			t.Fatalf("create file: %v", err)
		}
		ids = append(ids, f.ID)
	}
	return src, ids
}

func runSplit(t *testing.T, store *database.PebbleStore, srcID string, fileIDs ...string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"segment_ids":["` + strings.Join(fileIDs, `","`) + `"],"as_one_book":true,"title":"Split"}`
	c, w := splitReq(body)
	c.Params[0].Value = srcID
	handlers.NewVersionsHandler(store).SplitSegmentsToBooks(c)
	return w
}

// assertSourceUntouched checks that a refused split wrote nothing: the source
// still owns its path's lookup key, is the only live book there, and holds
// every row.
func assertSourceUntouched(t *testing.T, store *database.PebbleStore, src *database.Book, rows int) {
	t.Helper()
	owner, err := store.GetBookByFilePath(src.FilePath)
	if err != nil || owner == nil || owner.ID != src.ID {
		t.Fatalf("GetBookByFilePath(%q) = %+v, %v; want the source %s", src.FilePath, owner, err, src.ID)
	}
	live, err := store.LiveBookIDsAtPath(src.FilePath)
	if err != nil || len(live) != 1 || live[0] != src.ID {
		t.Fatalf("live books at %q = %v, %v; want only the source", src.FilePath, live, err)
	}
	files, err := store.GetBookFiles(src.ID)
	if err != nil || len(files) != rows {
		t.Fatalf("source rows = %d, %v; want %d", len(files), err, rows)
	}
}

// Scenario A: files from two sibling folders. Their common folder is the
// source's own path, so creating a book there would take the source's lookup
// key. Refused before any write.
func TestSplitAsOneBook_FilesFromTwoFolders_Refused(t *testing.T) {
	store := openSplitStore(t)
	src, ids := seedSplitBook(t, store, "/lib/Parent",
		"/lib/Parent/A/01.mp3", "/lib/Parent/B/01.mp3", "/lib/Parent/B/02.mp3")

	w := runSplit(t, store, src.ID, ids[0], ids[1])
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "split_files_span_folders") {
		t.Fatalf("want 400 split_files_span_folders, got %d: %s", w.Code, w.Body.String())
	}
	assertSourceUntouched(t, store, src, 3)
}

// Scenario B: the selected files sit directly in the source's folder, so the
// new book's path would be the source's path. Refused before any write; the
// source keeps its key (before the fix the new book took it, and the source's
// later path update deleted it, leaving the path with no owner).
func TestSplitAsOneBook_FolderIsSourcePath_Refused(t *testing.T) {
	store := openSplitStore(t)
	src, ids := seedSplitBook(t, store, "/lib/New",
		"/lib/New/01.mp3", "/lib/New/02.mp3", "/lib/Other/03.mp3")

	w := runSplit(t, store, src.ID, ids[0], ids[1])
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "split_path_taken") {
		t.Fatalf("want 400 split_path_taken, got %d: %s", w.Code, w.Body.String())
	}
	assertSourceUntouched(t, store, src, 3)
}

// The target folder already holds another live book: refused, and that book
// keeps its key.
func TestSplitAsOneBook_FolderHeldByAnotherBook_Refused(t *testing.T) {
	store := openSplitStore(t)
	src, ids := seedSplitBook(t, store, "/lib/Omnibus",
		"/lib/Omnibus/Book 1/01.mp3", "/lib/Omnibus/Book 1/02.mp3", "/lib/Omnibus/Book 2/01.mp3")
	other, err := store.CreateBook(&database.Book{Title: "Other", FilePath: "/lib/Omnibus/Book 1"})
	if err != nil {
		t.Fatal(err)
	}

	w := runSplit(t, store, src.ID, ids[0], ids[1])
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), other.ID) {
		t.Fatalf("want 400 naming %s, got %d: %s", other.ID, w.Code, w.Body.String())
	}
	assertSourceUntouched(t, store, src, 3)
	if got, _ := store.GetBookByFilePath("/lib/Omnibus/Book 1"); got == nil || got.ID != other.ID {
		t.Fatalf("occupant lost its key: %+v", got)
	}
}

// The remaining file is outside the source's folder, so the source's path
// moves to that file's folder: the write branch on a real store. The source
// takes the new folder's key and gives up its old one; the new book keeps
// its own.
func TestSplitAsOneBook_SourceMovesToRemainingFilesFolder(t *testing.T) {
	store := openSplitStore(t)
	src, ids := seedSplitBook(t, store, "/lib/Omnibus",
		"/lib/Omnibus/Book 1/01.mp3", "/lib/Omnibus/Book 1/02.mp3", "/lib/Other/03.mp3")

	w := runSplit(t, store, src.ID, ids[0], ids[1])
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	srcBook, err := store.GetBookByID(src.ID)
	if err != nil || srcBook == nil || srcBook.FilePath != "/lib/Other" {
		t.Fatalf("source = %+v, %v; want path /lib/Other", srcBook, err)
	}
	if owner, _ := store.GetBookByFilePath("/lib/Other"); owner == nil || owner.ID != src.ID {
		t.Fatalf("source does not own its new path key: %+v", owner)
	}
	if owner, _ := store.GetBookByFilePath("/lib/Omnibus"); owner != nil {
		t.Fatalf("the old path key still names %s after the source left", owner.ID)
	}
	moved, err := store.GetBookFileByPath("/lib/Omnibus/Book 1/01.mp3")
	if err != nil || moved == nil {
		t.Fatalf("moved row: %v", err)
	}
	if owner, _ := store.GetBookByFilePath("/lib/Omnibus/Book 1"); owner == nil || owner.ID != moved.BookID {
		t.Fatalf("new book does not own its path key: %+v", owner)
	}
}

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
	organized := "organized"
	src, err := store.CreateBook(&database.Book{Title: "Omnibus", FilePath: "/lib/Omnibus", Format: "mp3", Duration: &dur, FileSize: &size, LibraryState: &organized})
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
	if newBook.LibraryState == nil || *newBook.LibraryState != "organized" {
		t.Fatalf("new book library_state = %v, want the source's \"organized\"", newBook.LibraryState)
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
	// The remaining file is still inside the source's folder, so the source
	// keeps that folder (never the one file's path) and its lookup key.
	if srcBook.FilePath != "/lib/Omnibus" {
		t.Fatalf("source path %q, want the unchanged folder", srcBook.FilePath)
	}
	if owner, _ := store.GetBookByFilePath("/lib/Omnibus"); owner == nil || owner.ID != src.ID {
		t.Fatalf("source lost its path key: %+v", owner)
	}
	if owner, _ := store.GetBookByFilePath("/lib/Omnibus/Book 1"); owner == nil || owner.ID != newID {
		t.Fatalf("new book does not own its path key: %+v", owner)
	}
}
