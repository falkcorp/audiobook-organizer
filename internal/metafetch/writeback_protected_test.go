// file: internal/metafetch/writeback_protected_test.go
// version: 2.4.1
// guid: 0d5a8e37-b6c2-4f91-8a3e-c47f1d209b65
// last-edited: 2026-10-30

package metafetch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"
)

type dirChecker struct{ dir string }

func (d dirChecker) IsProtected(path string) bool {
	return strings.HasPrefix(path, d.dir+string(os.PathSeparator))
}

// unloadedChecker is a Deluge cache whose list never loaded: it reports
// nothing protected, and Loaded false.
type unloadedChecker struct{}

func (unloadedChecker) IsProtected(string) bool { return false }
func (unloadedChecker) Loaded() bool            { return false }

// countingImporter records every import request and hands back a library path.
type countingImporter struct{ calls []string }

func (c *countingImporter) ImportPath(_ context.Context, src, _ string) (string, error) {
	c.calls = append(c.calls, src)
	return filepath.Join(os.TempDir(), "library-root", filepath.Base(src)), nil
}

// Write-back's per-file write must REFUSE a protected file, never import it.
// With the production deps (importer wired) the 8a8a9ebe version routed the
// write through ResolvePathForWrite, which imported every Deluge file to
// RootDir/<basename> with no book folder and wrote the copy: multi-file books
// collided or half-imported, and a single-file book's row kept the seeding
// path. The importer must never be called and the writer must not run.
func TestWriteFileTagsSafe_ProtectedPathIsRefusedNeverImported(t *testing.T) {
	seeding := filepath.Join(t.TempDir(), "seeding")
	protectedFile := filepath.Join(seeding, "book.m4b")
	libraryFile := filepath.Join(t.TempDir(), "book.m4b")

	var written []string
	imp := &countingImporter{}
	svc := &Service{
		safeWriteDeps: tagger.SafeWriteDeps{ProtectedCache: dirChecker{dir: seeding}, Importer: imp},
		fileTagWrite: func(path string, _ map[string]any) error {
			written = append(written, path)
			return nil
		},
	}
	tags := map[string]any{"title": "T"}

	err := svc.writeFileTagsSafe(context.Background(), protectedFile, tags, fileops.WriteTagsSafeOptions{}, fileops.OperationConfig{})
	if !errors.Is(err, tagger.ErrProtectedPathWrite) {
		t.Fatalf("err = %v, want one wrapping tagger.ErrProtectedPathWrite", err)
	}
	if len(imp.calls) != 0 {
		t.Errorf("importer called for %v: write-back must never import file by file", imp.calls)
	}
	if err := svc.writeFileTagsSafe(context.Background(), libraryFile, tags, fileops.WriteTagsSafeOptions{}, fileops.OperationConfig{}); err != nil {
		t.Fatalf("unprotected write: %v", err)
	}
	if len(written) != 1 || written[0] != libraryFile {
		t.Errorf("writer called for %v, want only the unprotected %s", written, libraryFile)
	}
}

// isProtectedPath must know the Deluge save paths, so a Deluge-protected book
// takes the book-level path (library copy, or skip) and its files are
// skipped and counted. Before 2026-09-14 it knew only import roots and the
// iTunes library. An unloaded Deluge list must count as protected.
func TestIsProtectedPath_ConsultsDelugeCache(t *testing.T) {
	seeding := filepath.Join(t.TempDir(), "seeding")
	svc := &Service{safeWriteDeps: tagger.SafeWriteDeps{ProtectedCache: dirChecker{dir: seeding}}}
	if !svc.isProtectedPath(filepath.Join(seeding, "Book", "a.m4b")) {
		t.Error("Deluge save_path file not reported protected")
	}
	if svc.isProtectedPath(filepath.Join(t.TempDir(), "lib", "a.m4b")) {
		t.Error("unrelated file reported protected")
	}

	unloaded := &Service{safeWriteDeps: tagger.SafeWriteDeps{ProtectedCache: unloadedChecker{}}}
	if !unloaded.isProtectedPath(filepath.Join(t.TempDir(), "any.m4b")) {
		t.Error("with the Deluge list unloaded, every path must count as protected")
	}
}

// The apply rename leg drops entries whose source is a Deluge seeding file.
// Before 2026-09-14 dropProtectedRenameEntries used isProtectedPath without
// the Deluge list, so such a file went on to RenameFiles and was moved.
func TestDropProtectedRenameEntries_DropsDelugeSources(t *testing.T) {
	seeding := filepath.Join(t.TempDir(), "seeding")
	lib := t.TempDir()
	svc := &Service{safeWriteDeps: tagger.SafeWriteDeps{ProtectedCache: dirChecker{dir: seeding}}}
	entries := []FileRenameEntry{
		{SourcePath: filepath.Join(seeding, "a.m4b"), TargetPath: filepath.Join(lib, "A", "a.m4b")},
		{SourcePath: filepath.Join(lib, "b.m4b"), TargetPath: filepath.Join(lib, "B", "b.m4b")},
	}
	kept := svc.dropProtectedRenameEntries("book-1", entries)
	if len(kept) != 1 || kept[0].SourcePath != entries[1].SourcePath {
		t.Errorf("kept %+v, want only the library entry", kept)
	}
}

// With the Deluge list unloaded, making a library copy is refused: every book
// outside root_dir looks protected then, so a copy could be made of a book
// that needs none.
func TestProtectedListLoaded_ReflectsCache(t *testing.T) {
	if !(&Service{}).protectedListLoaded() {
		t.Error("no cache wired must count as loaded")
	}
	if (&Service{safeWriteDeps: tagger.SafeWriteDeps{ProtectedCache: unloadedChecker{}}}).protectedListLoaded() {
		t.Error("unloaded cache reported loaded")
	}
}

// A path spelled through a symlink into the iTunes library folder is protected,
// and an unrelated path next to a link is not.
func TestIsProtectedPath_SymlinkIntoITunesFolder(t *testing.T) {
	prev := config.AppConfig.ITunes.LibraryReadPath
	t.Cleanup(func() { config.AppConfig.ITunes.LibraryReadPath = prev })
	base := t.TempDir()
	itunes := filepath.Join(base, "lib", "Gotham Music")
	config.AppConfig.ITunes.LibraryReadPath = filepath.Join(itunes, "Library.xml")
	if err := os.MkdirAll(filepath.Join(itunes, "Hush"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(base, "lib", "Arkham", "Joker"), 0o755); err != nil {
		t.Fatal(err)
	}
	into := filepath.Join(base, "lib", "link")
	away := filepath.Join(base, "lib", "away")
	if err := os.Symlink(filepath.Join(itunes, "Hush"), into); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(base, "lib", "Arkham", "Joker"), away); err != nil {
		t.Fatal(err)
	}

	svc := &Service{}
	if !svc.isProtectedPath(filepath.Join(into, "hush.m4b")) {
		t.Error("a file reached through a symlink into the iTunes folder is not protected")
	}
	if svc.isProtectedPath(filepath.Join(away, "killing-joke.m4b")) {
		t.Error("a file reached through a symlink to an unprotected folder reported protected")
	}
}

// While the import-path list cannot be read, a file under an import path must not
// be reported unprotected: write-back and renames act on "not protected". Every
// path is protected until the list reads again.
func TestIsProtectedPath_UnreadableImportPathListFailsClosed(t *testing.T) {
	base := t.TempDir()
	imports := filepath.Join(base, "Gotham Imports")
	library := filepath.Join(base, "Arkham", "Joker", "killing-joke.m4b")
	inImports := filepath.Join(imports, "Hush", "hush.m4b")

	failing := true
	svc := &Service{db: &database.MockStore{
		GetAllImportPathsFunc: func() ([]database.ImportPath, error) {
			if failing {
				return nil, errors.New("pebble: closed")
			}
			return []database.ImportPath{{Path: imports}}, nil
		},
	}}

	if !svc.isProtectedPath(inImports) {
		t.Error("a file under an import path is not protected while the list is unreadable")
	}
	if !svc.isProtectedPath(library) {
		t.Error("a library file was vouched for while the list is unreadable")
	}

	failing = false
	if !svc.isProtectedPath(inImports) {
		t.Error("import path not protected once the list reads again")
	}
	if svc.isProtectedPath(library) {
		t.Error("library file still protected once the list reads again")
	}
}

// With the import-path list unreadable every book looks protected, so the
// createLibraryCopy policy must refuse (as it does for an unloaded Deluge list)
// instead of copying a book that may need no copy; it works again once the
// store reads.
func TestResolveLibraryCopy_RefusesWhileImportPathListUnreadable(t *testing.T) {
	libRoot := t.TempDir()
	orig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = orig })
	config.AppConfig.RootDir = libRoot

	book := &database.Book{ID: "b1", Title: "Hush", FilePath: filepath.Join(t.TempDir(), "Hush")}
	failing := true
	mock := &database.MockStore{
		GetAllImportPathsFunc: func() ([]database.ImportPath, error) {
			if failing {
				return nil, errors.New("pebble: closed")
			}
			return nil, nil
		},
		GetBookByIDFunc: func(string) (*database.Book, error) { b := *book; return &b, nil },
	}
	svc := NewService(mock)

	if svc.protectedListLoaded() {
		t.Fatal("protectedListLoaded true while the import-path list is unreadable")
	}
	got, err := svc.resolveLibraryCopy("b1", book, createLibraryCopy, func() error { return nil })
	if !errors.Is(err, tagger.ErrProtectedListNotLoaded) {
		t.Fatalf("err = %v, want ErrProtectedListNotLoaded", err)
	}
	if got != nil {
		t.Errorf("a library copy was returned: %+v", got)
	}
	if entries, _ := os.ReadDir(libRoot); len(entries) != 0 {
		t.Errorf("library root not empty: %v", entries)
	}

	failing = false
	if !svc.protectedListLoaded() {
		t.Error("protectedListLoaded false once the store reads again")
	}
	if _, err := svc.resolveLibraryCopy("b1", book, createLibraryCopy, func() error { return nil }); errors.Is(err, tagger.ErrProtectedListNotLoaded) {
		t.Errorf("still refused once the store reads: %v", err)
	}
}

// The import-path list is read once per decision. A store that fails its first
// read and succeeds on the second must still refuse: were existingLibraryCopy
// and the loaded check to read separately, the first would call the book
// protected for want of a list and the second would pass, so a book that was
// never protected would get a copy.
func TestResolveLibraryCopy_ReadsImportPathsOnce(t *testing.T) {
	libRoot := t.TempDir()
	orig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = orig })
	config.AppConfig.RootDir = libRoot

	book := &database.Book{ID: "b1", Title: "Hush", FilePath: filepath.Join(t.TempDir(), "Hush")}
	var reads int
	mock := &database.MockStore{
		GetAllImportPathsFunc: func() ([]database.ImportPath, error) {
			reads++
			if reads == 1 {
				return nil, errors.New("pebble: closed")
			}
			return nil, nil
		},
		GetBookByIDFunc: func(string) (*database.Book, error) { b := *book; return &b, nil },
	}
	svc := NewService(mock)

	got, err := svc.resolveLibraryCopy("b1", book, createLibraryCopy, func() error { return nil })
	if !errors.Is(err, tagger.ErrProtectedListNotLoaded) {
		t.Fatalf("err = %v, want ErrProtectedListNotLoaded", err)
	}
	if got != nil {
		t.Errorf("a library copy was returned: %+v", got)
	}
	if reads != 1 {
		t.Errorf("import paths read %d times, want 1", reads)
	}
	if entries, _ := os.ReadDir(libRoot); len(entries) != 0 {
		t.Errorf("library root not empty: %v", entries)
	}
}
