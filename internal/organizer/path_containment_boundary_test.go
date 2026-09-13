// file: internal/organizer/path_containment_boundary_test.go
// version: 1.0.0
// guid: c83e5a17-2b9f-4c64-8d01-f5a7e3b92c46
// last-edited: 2026-09-12

package organizer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The organizer decides "already in the library" from RootDir. A sibling
// directory whose name starts with the root ("<root>2") is NOT in the library.
// setupInPlace's root is a fresh temp dir, so root+"2" is a real sibling that
// the test's temp tree cleans up.

type infoRecordingLogger struct {
	noopLogger
	mu    sync.Mutex
	infos []string
}

func (l *infoRecordingLogger) Info(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.infos = append(l.infos, fmt.Sprintf(format, args...))
}

func (l *infoRecordingLogger) saw(sub string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, s := range l.infos {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func TestFilter_SiblingOfRootIsNotTreatedAsInRoot(t *testing.T) {
	svc, store, root := setupInPlace(t)
	sibling := root + "2"

	// A sibling book with no book_files: outside the root it must be held back
	// for lacking book_files (it cannot be copied), not waved through as an
	// in-root re-organize.
	noFiles, err := store.CreateBook(&database.Book{ID: "sib-nofiles", Title: "Sibling Title", FilePath: filepath.Join(sibling, "in", "a.mp3")})
	if err != nil {
		t.Fatal(err)
	}
	noFiles.Author = &database.Author{Name: "Some Author"}

	// A sibling book WITH book_files goes through the outside-root branch and
	// is organized; it must not be logged as "in RootDir".
	withFiles := addInPlaceBook(t, store, "sib-files", "Other Title", filepath.Join(sibling, "in", "b.mp3"), filled(100, 3), nil, 0)

	log := &infoRecordingLogger{}
	toOrganize, _, _ := svc.filterBooksNeedingOrganization([]database.Book{*noFiles, *withFiles}, log)
	if len(toOrganize) != 1 || toOrganize[0].ID != withFiles.ID {
		ids := []string{}
		for _, b := range toOrganize {
			ids = append(ids, b.ID)
		}
		t.Errorf("toOrganize = %v, want only %s", ids, withFiles.ID)
	}
	if log.saw("Book in RootDir needs re-organization") {
		t.Errorf("a book under %s was logged as being in RootDir %s", sibling, root)
	}
}

func TestOrganizeOneBook_SiblingOfRootIsCopiedNotMovedInPlace(t *testing.T) {
	svc, store, root := setupInPlace(t)
	src := filepath.Join(root+"2", "in", "x.mp3")
	b := addInPlaceBook(t, store, "sib", "Title", src, filled(2048, 0x5A), nil, 0)

	landing, _ := svc.OrganizeOneBook(svc.newOrganizer(), b, &noopLogger{})
	if landing != nil && landing.InPlace {
		t.Errorf("book at %s was re-organized in place as if it were inside %s", src, root)
	}
	if _, err := os.Stat(src); err != nil {
		t.Errorf("source outside the library root was moved away: %v", err)
	}
}

func TestPreviewOrganize_SiblingOfRootNeedsCopy(t *testing.T) {
	_, store, root := setupInPlace(t)
	b := addInPlaceBook(t, store, "sib", "Title", filepath.Join(root+"2", "in", "x.mp3"), filled(100, 7), nil, 0)
	// PreviewOrganize reloads the book from the store; give it a stored
	// author so the author gate does not zero NeedsCopy for its own reason.
	author, err := store.CreateAuthor("Some Author")
	if err != nil {
		t.Fatal(err)
	}
	b.AuthorID = &author.ID
	if _, err := store.UpdateBook(b.ID, b); err != nil {
		t.Fatal(err)
	}

	resp, err := NewPreviewService(store).PreviewOrganize(b.ID)
	if err != nil {
		t.Fatalf("PreviewOrganize: %v", err)
	}
	if !resp.NeedsCopy {
		t.Errorf("NeedsCopy = false for %s; it is outside %s", resp.CurrentPath, root)
	}
}

func TestOrganizeBook_HashTwinInSiblingIsNotADuplicateInLibrary(t *testing.T) {
	_, store, root := setupInPlace(t)
	hash := "boundary-hash-1"

	twinPath := filepath.Join(root+"2", "Some Author", "Title", "Title.mp3")
	if _, err := store.CreateBook(&database.Book{ID: "twin", Title: "Title", FilePath: twinPath, FileHash: &hash}); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "Title.mp3")
	if err := os.WriteFile(src, filled(512, 9), 0o644); err != nil {
		t.Fatal(err)
	}
	// Stored WITHOUT the hash so the store's hash index still names the twin;
	// the hash is set on the in-memory copy OrganizeBook reads.
	book, err := store.CreateBook(&database.Book{ID: "new", Title: "Title", FilePath: src})
	if err != nil {
		t.Fatal(err)
	}
	book.FileHash = &hash
	book.Author = &database.Author{Name: "Some Author"}
	if twin, _ := store.GetBookByFileHash(hash); twin == nil || twin.ID != "twin" {
		t.Fatalf("fixture: hash lookup should find the twin, got %+v", twin)
	}

	org := NewOrganizer(&config.AppConfig)
	org.SetStore(store)
	if _, _, err := org.OrganizeBook(book); err != nil && strings.Contains(err.Error(), "duplicate file already organized") {
		t.Errorf("hash twin at %s (outside %s) was reported as already organized: %v", twinPath, root, err)
	}
}

func TestDetectFragmentCollapse_IgnoresSiblingOfRoot(t *testing.T) {
	svc, store, root := setupInPlace(t)
	mk := func(dir, id string, n int) database.Book {
		return *addInPlaceBook(t, store, id, "Title", filepath.Join(dir, fmt.Sprintf("Title - %d.mp3", n)), filled(64, byte(n)), nil, 0)
	}

	sib := filepath.Join(root+"2", "in")
	got := svc.detectFragmentCollapse(context.Background(), []database.Book{mk(sib, "s1", 1), mk(sib, "s2", 2)})
	if len(got) != 0 {
		t.Errorf("detectFragmentCollapse considered books under %s2 as in-library: %v", root, got)
	}

	// Control: the same layout inside the root is detected.
	in := filepath.Join(root, "in")
	got = svc.detectFragmentCollapse(context.Background(), []database.Book{mk(in, "r1", 1), mk(in, "r2", 2)})
	if len(got) != 2 {
		t.Fatalf("control: detectFragmentCollapse inside the root = %v, want 2 entries", got)
	}
}
