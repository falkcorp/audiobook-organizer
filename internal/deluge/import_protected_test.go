// file: internal/deluge/import_protected_test.go
// version: 1.0.0
// guid: 9a4e2c71-5b3d-4f08-b6e2-17d0c8a9f354
// last-edited: 2026-09-14
//
// ImportToLibrary with a protected-path predicate: the already-imported
// branch and the source-is-destination branch must never hand a protected
// path back as the library path.

package deluge

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"
)

// dirProtected protects every path under dir, the shape of
// ProtectedPathCache for one save_path.
type dirProtected struct{ dir string }

func (d dirProtected) IsProtected(path string) bool {
	return strings.HasPrefix(path, d.dir+string(os.PathSeparator))
}

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("audio data"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A row marked imported whose FilePath was pointed back at the seeding copy.
// Before 2026-09-14 ImportToLibrary returned that protected path, the tag
// guard refused every write to it, and nothing ever re-imported it: the book
// could never be tagged. It must be copied into the library and repointed.
func TestImportToLibrary_AlreadyImportedRowNamingProtectedPath_IsReimported(t *testing.T) {
	seeding := t.TempDir()
	src := filepath.Join(seeding, "book.m4b")
	writeFile(t, src)
	root := t.TempDir()
	store := &fakeDelugStore{}
	importedAt := time.Now().Add(-time.Hour)
	bf := &database.BookFile{ID: "id-re", FilePath: src, ImportedFromDelugeAt: &importedAt}

	newPath, err := ImportToLibrary(&config.Config{RootDir: root}, nil, store, bf, dirProtected{dir: seeding})
	if err != nil {
		t.Fatalf("ImportToLibrary: %v", err)
	}
	want := filepath.Join(root, "book.m4b")
	if newPath != want {
		t.Fatalf("newPath = %q, want the library copy %q (not the protected source)", newPath, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Errorf("library copy missing: %v", err)
	}
	if store.updated == nil || store.updated.FilePath != want || store.updated.DelugeOriginalPath != src {
		t.Errorf("row not repointed to the copy: %+v", store.updated)
	}
	if _, err := os.Stat(src); err != nil {
		t.Errorf("protected source must be left in place: %v", err)
	}
}

// An already-imported row naming a library (unprotected) file stays a no-op
// when a checker is wired: the idempotency guard is unchanged for it.
func TestImportToLibrary_AlreadyImportedRowNamingLibraryPath_StaysNoOp(t *testing.T) {
	root := t.TempDir()
	lib := filepath.Join(root, "book.m4b")
	writeFile(t, lib)
	store := &fakeDelugStore{}
	importedAt := time.Now().Add(-time.Hour)
	bf := &database.BookFile{ID: "id-lib", FilePath: lib, ImportedFromDelugeAt: &importedAt}

	newPath, err := ImportToLibrary(&config.Config{RootDir: root}, nil, store, bf, dirProtected{dir: t.TempDir()})
	if err != nil || newPath != lib || store.updated != nil {
		t.Errorf("got (%q, %v, updated=%v), want (%q, nil, no update)", newPath, err, store.updated, lib)
	}
}

// A protected directory under RootDir: the source is its own destination, so
// there is nowhere to copy it. Before 2026-09-14 the protected source came
// back as the "library" path. Both a fresh row and an already-imported row
// must be refused with tagger.ErrProtectedPathWrite, with no row update.
func TestImportToLibrary_ProtectedSourceIsItsOwnDestination_Refused(t *testing.T) {
	for _, alreadyImported := range []bool{false, true} {
		name := "fresh row"
		if alreadyImported {
			name = "already-imported row"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			seeding := filepath.Join(root, "seeding")
			src := filepath.Join(seeding, "book.m4b")
			writeFile(t, src)
			store := &fakeDelugStore{}
			bf := &database.BookFile{ID: "id-same", FilePath: src}
			if alreadyImported {
				at := time.Now().Add(-time.Hour)
				bf.ImportedFromDelugeAt = &at
			}

			newPath, err := ImportToLibrary(&config.Config{RootDir: root}, nil, store, bf, dirProtected{dir: seeding})
			if !errors.Is(err, tagger.ErrProtectedPathWrite) {
				t.Fatalf("err = %v, want one wrapping tagger.ErrProtectedPathWrite", err)
			}
			if newPath != "" {
				t.Errorf("newPath = %q, want empty: the protected path must not be returned", newPath)
			}
			if store.updated != nil {
				t.Errorf("UpdateBookFile called: %+v", store.updated)
			}
		})
	}
}
