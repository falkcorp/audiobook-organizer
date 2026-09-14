// file: internal/metadata/write_protected_no_import_test.go
// version: 1.0.0
// guid: 5d8b2e41-9c7a-4f63-a0e5-1b3f6c9d2a78
// last-edited: 2026-09-14

package metadata

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"
)

// countingImporter records every import the guard asks for. Returning a path
// under RootDir is what the real adapter did: copy the seeding file there.
type countingImporter struct {
	calls atomic.Int32
	dest  string
}

func (c *countingImporter) ImportPath(_ context.Context, _ string, _ string) (string, error) {
	c.calls.Add(1)
	return c.dest, nil
}

// The server wires a Deluge LibraryImporterAdapter into SetSafeWriteDeps. Every
// package-level write (single-tag fixes, tag reverts, WriteMetadataToFile) used
// to hand a protected path to it, which copied the seeding file to
// RootDir/<basename> and repointed the row there. The package must drop the
// importer and refuse the write instead.
func TestPackageWrites_ProtectedPathRefusedNeverImported(t *testing.T) {
	prev := packageSafeWriteDeps
	t.Cleanup(func() { packageSafeWriteDeps = prev })

	seeding := filepath.Join(t.TempDir(), "seeding")
	if err := os.MkdirAll(seeding, 0o755); err != nil {
		t.Fatal(err)
	}
	imp := &countingImporter{dest: filepath.Join(t.TempDir(), "scattered.m4b")}
	SetSafeWriteDeps(tagger.SafeWriteDeps{ProtectedCache: dirProtected{dir: seeding}, Importer: imp})

	f := filepath.Join(seeding, "book.m4b")
	content := []byte("not real audio")
	if err := os.WriteFile(f, content, 0o644); err != nil {
		t.Fatal(err)
	}

	writes := map[string]func() error{
		"WriteSingleTag":      func() error { return WriteSingleTag(f, "COMPOSER", "x") },
		"WriteMetadataToFile": func() error { return WriteMetadataToFile(f, map[string]any{"title": "T"}, fileops.OperationConfig{}) },
	}
	for name, write := range writes {
		if err := write(); !errors.Is(err, tagger.ErrProtectedPathWrite) {
			t.Errorf("%s: err = %v, want one wrapping tagger.ErrProtectedPathWrite", name, err)
		}
	}
	if n := imp.calls.Load(); n != 0 {
		t.Errorf("the importer was called %d time(s); a protected write must be refused, never imported", n)
	}
	got, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Error("protected file changed")
	}
}
