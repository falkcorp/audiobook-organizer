// file: internal/tagger/protected_write_refusal_test.go
// version: 1.0.0
// guid: 5c9a2e17-4d83-4f6b-a1e0-8b3d7c6f2a94
// last-edited: 2026-09-13

package tagger

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Review round 5, item 5: ImportToLibrary returns the row's current path when
// the row is already marked imported. If that row names the protected source
// again, the importer hands the protected path back, and the tag write landed
// in place on a file a torrent client is seeding. recordingImporter with no
// returnPath models exactly that: it returns the path it was given.
func TestResolvePath_ImportReturningProtectedPathIsRefused(t *testing.T) {
	t.Parallel()
	protectedSrc := "/deluge/books/qux.m4b"
	checker := &staticPathChecker{protected: map[string]bool{protectedSrc: true}}
	importer := &recordingImporter{}

	got, err := resolvePath(context.Background(), protectedSrc, SafeWriteDeps{ProtectedCache: checker, Importer: importer})
	if !errors.Is(err, ErrProtectedPathWrite) {
		t.Fatalf("err = %v, want ErrProtectedPathWrite", err)
	}
	if got != "" {
		t.Errorf("path = %q, want none", got)
	}
	if len(importer.calls) != 1 {
		t.Errorf("ImportPath called %d time(s); want 1", len(importer.calls))
	}
}

// The refusal comes before any byte is written: the protected file keeps its
// exact contents and no temp file is left beside it.
func TestWriteTagsSafe_ProtectedResultLeavesFileUntouched(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := filepath.Join(dir, "seeding.m4b")
	orig := []byte("torrent payload bytes")
	if err := os.WriteFile(src, orig, 0o644); err != nil {
		t.Fatal(err)
	}
	checker := &staticPathChecker{protected: map[string]bool{src: true}}

	err := WriteTagsSafe(context.Background(), src, map[string][]string{"TITLE": {"x"}}, 0,
		SafeWriteDeps{ProtectedCache: checker, Importer: &recordingImporter{}})
	if !errors.Is(err, ErrProtectedPathWrite) {
		t.Fatalf("err = %v, want ErrProtectedPathWrite", err)
	}
	got, err := os.ReadFile(src)
	if err != nil || !bytes.Equal(got, orig) {
		t.Fatalf("protected file changed: %q (err %v), want %q", got, err, orig)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("dir holds %d entries after the refused write, want only the source", len(entries))
	}
}
