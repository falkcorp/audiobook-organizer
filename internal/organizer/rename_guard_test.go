// file: internal/organizer/rename_guard_test.go
// version: 1.0.0
// guid: 4e7b2a93-1d5c-4f86-9a0e-b3c8d61f2e74
// last-edited: 2026-09-14

package organizer

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// RenameFiles had no protected-path check of its own, so a Deluge seeding
// file that reached it (the apply rename leg's filter did not know the
// Deluge save paths) was moved with os.Rename. With the guard set, a plan
// that moves a protected file is refused whole, before anything moves.
func TestRenameFiles_RefusesPlanThatMovesProtectedSource(t *testing.T) {
	dir := t.TempDir()
	seeding := filepath.Join(dir, "seeding")
	lib := filepath.Join(dir, "lib")
	for _, d := range []string{seeding, lib} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	protectedSrc := filepath.Join(seeding, "a.m4b")
	librarySrc := filepath.Join(lib, "b.m4b")
	for _, f := range []string{protectedSrc, librarySrc} {
		if err := os.WriteFile(f, []byte("audio"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(SetRenameSourceGuard(func(p string) bool {
		return strings.HasPrefix(p, seeding+string(os.PathSeparator))
	}))

	entries := []FileRenameEntry{
		{SourcePath: librarySrc, TargetPath: filepath.Join(lib, "B", "b.m4b")},
		{SourcePath: protectedSrc, TargetPath: filepath.Join(lib, "A", "a.m4b")},
	}
	_, err := RenameFiles(entries, nil)
	if !errors.Is(err, ErrProtectedRenameSource) {
		t.Fatalf("err = %v, want ErrProtectedRenameSource", err)
	}
	for _, f := range []string{protectedSrc, librarySrc} {
		if _, statErr := os.Stat(f); statErr != nil {
			t.Errorf("%s moved or removed: %v (nothing may move)", f, statErr)
		}
	}
	for _, e := range entries {
		if _, statErr := os.Stat(e.TargetPath); statErr == nil {
			t.Errorf("target %s created", e.TargetPath)
		}
	}
}

// An entry already at its target moves nothing, so a protected file there is
// not a reason to refuse.
func TestRenameFiles_ProtectedEntryAlreadyInPlaceIsNotRefused(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "a.m4b")
	if err := os.WriteFile(f, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(SetRenameSourceGuard(func(string) bool { return true }))
	if _, err := RenameFiles([]FileRenameEntry{{SourcePath: f, TargetPath: f}}, nil); err != nil {
		t.Fatalf("in-place entry refused: %v", err)
	}
}
