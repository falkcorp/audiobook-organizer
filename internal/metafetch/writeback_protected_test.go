// file: internal/metafetch/writeback_protected_test.go
// version: 2.0.0
// guid: 0d5a8e37-b6c2-4f91-8a3e-c47f1d209b65
// last-edited: 2026-09-14

package metafetch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

	err := svc.writeFileTagsSafe(protectedFile, tags, fileops.WriteTagsSafeOptions{}, fileops.OperationConfig{})
	if !errors.Is(err, tagger.ErrProtectedPathWrite) {
		t.Fatalf("err = %v, want one wrapping tagger.ErrProtectedPathWrite", err)
	}
	if len(imp.calls) != 0 {
		t.Errorf("importer called for %v: write-back must never import file by file", imp.calls)
	}
	if err := svc.writeFileTagsSafe(libraryFile, tags, fileops.WriteTagsSafeOptions{}, fileops.OperationConfig{}); err != nil {
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
