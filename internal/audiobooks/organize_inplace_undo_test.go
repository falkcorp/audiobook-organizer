// file: internal/audiobooks/organize_inplace_undo_test.go
// version: 1.0.0
// guid: 8d5e0c3a-2f41-4b7a-9e16-5a3c7b9d2e08
// last-edited: 2026-09-28

package audiobooks

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
)

// organizeUndoFixture is a real store (memdb serving, as in prod after
// warmup), a library root and the organizer service over it.
func organizeUndoFixture(t *testing.T) (*database.PebbleStore, *organizer.Service, string) {
	t.Helper()
	prev := config.AppConfig
	t.Cleanup(func() { config.AppConfig = prev })
	root := t.TempDir()
	config.AppConfig = config.Config{
		RootDir:             root,
		FolderNamingPattern: "{author}/{title}",
		FileNamingPattern:   "{title}",
	}
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.WaitForWarmup()
	store.UseMemDB = true
	return store, organizer.NewService(store), root
}

func writeAudio(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o775); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// organizeAndUndo organizes book in place, commits the landing under an
// operation, then reverts that operation, and returns the landing.
func organizeAndUndo(t *testing.T, store *database.PebbleStore, svc *organizer.Service, book *database.Book, afterOrganize func(*organizer.Landing)) *organizer.Landing {
	t.Helper()
	log := logger.New("test")
	landing, err := svc.OrganizeOneBook(organizer.NewOrganizer(&config.AppConfig), book, log)
	if err != nil {
		t.Fatalf("OrganizeOneBook: %v", err)
	}
	const opID = "op-undo-roundtrip"
	if _, _, err := svc.CommitLanding(book, landing, opID, log); err != nil {
		t.Fatalf("CommitLanding: %v", err)
	}
	afterOrganize(landing)
	res, err := NewRevertService(store).RevertOperation(opID)
	if err != nil {
		t.Fatalf("RevertOperation: %v", err)
	}
	if res.Failed != 0 || res.NotRestorable != 0 {
		t.Fatalf("revert result %+v: every row of an in-place organize must be restorable and restored", res)
	}
	return landing
}

// TestOrganizeInPlace_DirectoryMoveUndoRoundTrip is N4 of the 2026-09-28
// review: with CommitLanding now recording the real old path, an in-place
// directory move produces an organize_rename the undo can act on. Organize,
// then undo, must leave the folder, the book and every row where they began.
func TestOrganizeInPlace_DirectoryMoveUndoRoundTrip(t *testing.T) {
	store, svc, root := organizeUndoFixture(t)
	dir := filepath.Join(root, "incoming", "Eldest")
	files := []string{filepath.Join(dir, "01.mp3"), filepath.Join(dir, "02.mp3")}
	book, err := store.CreateBook(&database.Book{ID: "dirbook", Title: "Eldest", FilePath: dir})
	if err != nil {
		t.Fatal(err)
	}
	for i, f := range files {
		writeAudio(t, f, string(rune('a'+i)))
		if err := store.CreateBookFile(&database.BookFile{ID: "dirbook-" + filepath.Base(f), BookID: book.ID, FilePath: f}); err != nil {
			t.Fatal(err)
		}
	}
	book.Author = &database.Author{Name: "Christopher Paolini"}

	organizeAndUndo(t, store, svc, book, func(l *organizer.Landing) {
		if l.Path == dir {
			t.Fatalf("the directory did not move")
		}
		if _, err := os.Stat(filepath.Join(l.Path, "01.mp3")); err != nil {
			t.Fatalf("file not at the organized path: %v", err)
		}
	})

	for _, f := range files {
		if _, err := os.Stat(f); err != nil {
			t.Fatalf("%s not restored: %v", f, err)
		}
	}
	got, err := store.GetBookByID(book.ID)
	if err != nil || got == nil || got.FilePath != dir {
		t.Fatalf("book path after undo = %v (err %v), want %q", got, err, dir)
	}
	rows, err := store.GetBookFiles(book.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if filepath.Dir(r.FilePath) != dir {
			t.Fatalf("row %s at %q after undo, want under %q", r.ID, r.FilePath, dir)
		}
	}
}

// TestOrganizeInPlace_MultiFileMoveUndoRoundTrip: the multi-file in-place
// move (S3) records one book_file_move per file plus a book_path_update, and
// the undo puts every file, every row and the book's path back.
func TestOrganizeInPlace_MultiFileMoveUndoRoundTrip(t *testing.T) {
	store, svc, root := organizeUndoFixture(t)
	dir := filepath.Join(root, "incoming", "Shared")
	files := []string{filepath.Join(dir, "Eldest 01.mp3"), filepath.Join(dir, "Eldest 02.mp3")}
	book, err := store.CreateBook(&database.Book{ID: "multi", Title: "Eldest", FilePath: files[0]})
	if err != nil {
		t.Fatal(err)
	}
	for i, f := range files {
		writeAudio(t, f, string(rune('a'+i)))
		if err := store.CreateBookFile(&database.BookFile{ID: "multi-" + string(rune('1'+i)), BookID: book.ID, FilePath: f}); err != nil {
			t.Fatal(err)
		}
	}
	// A neighbour in the shared folder, which must not move either way.
	neighbour := filepath.Join(dir, "Other.mp3")
	writeAudio(t, neighbour, "z")
	book.Author = &database.Author{Name: "Christopher Paolini"}

	organizeAndUndo(t, store, svc, book, func(l *organizer.Landing) {
		if !l.MultiFile || len(l.FileMoves) != 2 {
			t.Fatalf("landing = %+v, want a two-file multi-file landing", l)
		}
		for _, f := range files {
			if _, err := os.Stat(f); !os.IsNotExist(err) {
				t.Fatalf("%s did not move (%v)", f, err)
			}
		}
	})

	for _, f := range append(files, neighbour) {
		if _, err := os.Stat(f); err != nil {
			t.Fatalf("%s not in place after undo: %v", f, err)
		}
	}
	got, err := store.GetBookByID(book.ID)
	if err != nil || got == nil || got.FilePath != files[0] {
		t.Fatalf("book path after undo = %v (err %v), want %q", got, err, files[0])
	}
	rows, err := store.GetBookFiles(book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(files) {
		t.Fatalf("%d rows after undo, want %d", len(rows), len(files))
	}
	for _, r := range rows {
		if filepath.Dir(r.FilePath) != dir {
			t.Fatalf("row %s at %q after undo, want under %q", r.ID, r.FilePath, dir)
		}
	}
}
