// file: internal/organizer/inplace_protected_symlink_test.go
// version: 1.0.0
// guid: 4a7d19c3-62be-4f80-b5e1-9d03c8a6f742
// last-edited: 2026-10-29

package organizer

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
)

// linkedLibraryDir makes root/incoming/link a symlink to realDir and returns
// the link. The book's path is then spelled inside the library root while the
// file is somewhere else: moveExclusive (link, then unlink) on such a path takes
// the real file out of its real folder.
func linkedLibraryDir(t *testing.T, root, realDir string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "incoming"), 0o775); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "incoming", "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatal(err)
	}
	return link
}

// guardProtecting installs a rename-source guard that protects dir the way the
// server's does (symlinks resolved), and restores the previous one afterwards.
func guardProtecting(t *testing.T, dir string) {
	t.Helper()
	restore := SetRenameSourceGuard(func(p string) bool {
		protected, _ := pathutil.UnderProtectedPath(p, []string{dir})
		return protected
	})
	t.Cleanup(restore)
}

func requireStillThere(t *testing.T, store *database.PebbleStore, book *database.Book, before string, real ...string) {
	t.Helper()
	for _, p := range real {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s moved or vanished: %v", p, err)
		}
	}
	if got := getInPlaceBook(t, store, book.ID); got.FilePath != before {
		t.Fatalf("book path rewritten to %q on a refused move", got.FilePath)
	}
}

// A library path spelled through a symlink into the frozen iTunes tree: the
// spelled path shows no books/itunes/ segment, the resolved one does.
func TestReOrganizeInPlace_RefusesAFileReachedThroughASymlinkIntoTheFrozenITunesTree(t *testing.T) {
	svc, store, root := setupInPlace(t)
	frozen := filepath.Join(t.TempDir(), "books", "itunes", "iTunes Media", "Audiobooks", "Someone")
	real := filepath.Join(frozen, "hush.m4b")
	if err := os.MkdirAll(frozen, 0o775); err != nil {
		t.Fatal(err)
	}
	link := linkedLibraryDir(t, root, frozen)
	book := addInPlaceBook(t, store, "hush", "Hush", filepath.Join(link, "hush.m4b"), filled(120, 3), nil, 0)
	before := book.FilePath

	_, err := svc.ReOrganizeInPlace(book, &noopLogger{})
	var conflict *DestinationConflictError
	if !errors.As(err, &conflict) || conflict.Category != OutcomeFrozenITunes {
		t.Fatalf("err = %v, want a %s refusal", err, OutcomeFrozenITunes)
	}
	requireStillThere(t, store, book, before, real)
}

// A library path spelled through a symlink into an iTunes library folder that
// only the server's protected-path check knows about.
func TestReOrganizeInPlace_RefusesAFileReachedThroughASymlinkIntoAProtectedFolder(t *testing.T) {
	svc, store, root := setupInPlace(t)
	itunes := filepath.Join(t.TempDir(), "Gotham Music")
	guardProtecting(t, itunes)
	real := filepath.Join(itunes, "Hush", "hush.m4b")
	if err := os.MkdirAll(filepath.Dir(real), 0o775); err != nil {
		t.Fatal(err)
	}
	link := linkedLibraryDir(t, root, filepath.Dir(real))
	book := addInPlaceBook(t, store, "hush", "Hush", filepath.Join(link, "hush.m4b"), filled(120, 3), nil, 0)
	before := book.FilePath

	_, err := svc.ReOrganizeInPlace(book, &noopLogger{})
	var conflict *DestinationConflictError
	if !errors.As(err, &conflict) || conflict.Category != OutcomeProtectedSource {
		t.Fatalf("err = %v, want a %s refusal", err, OutcomeProtectedSource)
	}
	requireStillThere(t, store, book, before, real)
}

// Control: with the guard installed, a book reached through a link to an
// unprotected folder still moves.
func TestReOrganizeInPlace_AFileThroughASymlinkToAnUnprotectedFolderStillMoves(t *testing.T) {
	svc, store, root := setupInPlace(t)
	guardProtecting(t, filepath.Join(t.TempDir(), "Gotham Music"))
	free := filepath.Join(t.TempDir(), "Arkham")
	if err := os.MkdirAll(free, 0o775); err != nil {
		t.Fatal(err)
	}
	link := linkedLibraryDir(t, root, free)
	book := addInPlaceBook(t, store, "joker", "Killing Joke", filepath.Join(link, "killing-joke.m4b"), filled(120, 3), nil, 0)

	newPath, err := svc.ReOrganizeInPlace(book, &noopLogger{})
	if err != nil {
		t.Fatalf("move refused: %v", err)
	}
	if _, statErr := os.Stat(newPath); statErr != nil {
		t.Fatalf("book not at its new path %s: %v", newPath, statErr)
	}
}

// A multi-file book moves its files one by one; one of them spelled through a
// link into the frozen tree stops the whole move before any file is touched.
func TestReOrganizeInPlace_MultiFileRefusesAFileReachedThroughASymlinkIntoTheFrozenITunesTree(t *testing.T) {
	svc, store, root := setupInPlace(t)
	book, first, _ := multiFileBook(t, store, root, "Knightfall")
	frozen := filepath.Join(t.TempDir(), "books", "itunes", "iTunes Media", "Audiobooks", "Someone")
	real := filepath.Join(frozen, "03.mp3")
	if err := os.MkdirAll(frozen, 0o775); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(real, filled(150, 6), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(filepath.Dir(first), "link")
	if err := os.Symlink(frozen, link); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBookFile(&database.BookFile{ID: "multi-3", BookID: book.ID, FilePath: filepath.Join(link, "03.mp3")}); err != nil {
		t.Fatal(err)
	}

	_, err := svc.ReOrganizeInPlace(book, &noopLogger{})
	var conflict *DestinationConflictError
	if !errors.As(err, &conflict) || conflict.Category != OutcomeFrozenITunes {
		t.Fatalf("err = %v, want a %s refusal", err, OutcomeFrozenITunes)
	}
	requireStillThere(t, store, book, first, real, first)
}

// A source whose symlinks cannot be resolved is an error, not a pass.
func TestRefuseProtectedInPlaceSource_UnresolvableSymlinksFailClosed(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	if err := os.Symlink(b, a); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(a, b); err != nil {
		t.Fatal(err)
	}
	err := refuseProtectedInPlaceSource(filepath.Join(a, "x.m4b"), filepath.Join(dir, "target.m4b"))
	if err == nil {
		t.Fatal("a source with a link cycle was passed")
	}
	var conflict *DestinationConflictError
	if errors.As(err, &conflict) {
		t.Fatalf("an unresolvable source is an error, not a %s refusal: %v", conflict.Category, err)
	}
}
