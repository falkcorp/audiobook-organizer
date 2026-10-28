// file: internal/versions/fs_test.go
// version: 1.0.1
// guid: 4e2c8a1d-5b9f-4f70-a7c6-2d8e0f1b9a57
// last-edited: 2026-10-28

package versions

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o775); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func fileContent(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func TestMoveToVersionsDir_SingleFile(t *testing.T) {
	bookDir := t.TempDir()
	orig := filepath.Join(bookDir, "Book.m4b")
	writeFile(t, orig, "audio-v1")

	newPaths, errs := MoveToVersionsDir(bookDir, "ver1", []string{orig})
	if len(errs) > 0 {
		t.Fatalf("errs: %v", errs)
	}
	if len(newPaths) != 1 {
		t.Fatalf("newPaths = %v", newPaths)
	}
	expected := filepath.Join(bookDir, ".versions", "ver1", "Book.m4b")
	if newPaths[0] != expected {
		t.Errorf("new path = %s, want %s", newPaths[0], expected)
	}
	if _, err := os.Stat(orig); !os.IsNotExist(err) {
		t.Errorf("source still exists at %s", orig)
	}
	if got := fileContent(t, expected); got != "audio-v1" {
		t.Errorf("content = %q", got)
	}
}

func TestMoveToVersionsDir_MultiFile(t *testing.T) {
	bookDir := t.TempDir()
	var srcs []string
	for i, name := range []string{"01.mp3", "02.mp3", "03.mp3"} {
		p := filepath.Join(bookDir, name)
		writeFile(t, p, string(rune('a'+i)))
		srcs = append(srcs, p)
	}

	newPaths, errs := MoveToVersionsDir(bookDir, "ver-multi", srcs)
	if len(errs) > 0 {
		t.Fatalf("errs: %v", errs)
	}
	if len(newPaths) != 3 {
		t.Fatalf("newPaths = %v", newPaths)
	}
	for i, np := range newPaths {
		if _, err := os.Stat(np); err != nil {
			t.Errorf("file %d not at new path: %v", i, err)
		}
	}
}

func TestMoveToVersionsDir_IdempotentOnRepeat(t *testing.T) {
	bookDir := t.TempDir()
	orig := filepath.Join(bookDir, "Book.m4b")
	writeFile(t, orig, "audio")

	// First move.
	_, errs1 := MoveToVersionsDir(bookDir, "ver1", []string{orig})
	if len(errs1) > 0 {
		t.Fatalf("first errs: %v", errs1)
	}
	// Second move (simulating a resumed operation). The source no
	// longer exists; should be a no-op, not an error.
	_, errs2 := MoveToVersionsDir(bookDir, "ver1", []string{orig})
	if len(errs2) > 0 {
		t.Errorf("repeat errs: %v (expected idempotent)", errs2)
	}
}

func TestMoveFromVersionsDir_ReverseRoundTrip(t *testing.T) {
	bookDir := t.TempDir()
	orig := filepath.Join(bookDir, "Book.m4b")
	writeFile(t, orig, "audio-content")

	// Move into versions slot.
	newPaths, errs := MoveToVersionsDir(bookDir, "ver1", []string{orig})
	if len(errs) > 0 {
		t.Fatalf("move to: %v", errs)
	}

	// Move back out (simulating a swap-to-primary).
	backPaths, errs := MoveFromVersionsDir(bookDir, "ver1", newPaths)
	if len(errs) > 0 {
		t.Fatalf("move from: %v", errs)
	}
	if backPaths[0] != orig {
		t.Errorf("back path = %s, want %s", backPaths[0], orig)
	}
	if got := fileContent(t, orig); got != "audio-content" {
		t.Errorf("roundtrip content lost: %q", got)
	}
}

func TestMoveToVersionsDir_RefusesClobber(t *testing.T) {
	bookDir := t.TempDir()
	orig := filepath.Join(bookDir, "Book.m4b")
	writeFile(t, orig, "v1")
	// Pre-create conflicting destination.
	slot := VersionSlotDir(bookDir, "ver1")
	if err := os.MkdirAll(slot, 0o775); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(slot, "Book.m4b"), []byte("preexisting"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, errs := MoveToVersionsDir(bookDir, "ver1", []string{orig})
	if len(errs) == 0 {
		t.Error("expected error on clobber")
	}
}

func TestPruneEmptyVersionsDir(t *testing.T) {
	bookDir := t.TempDir()
	slot := VersionSlotDir(bookDir, "ver1")
	_ = os.MkdirAll(slot, 0o775)

	// Dir non-empty → prune is no-op.
	if err := PruneEmptyVersionsDir(bookDir); err != nil {
		t.Fatalf("prune non-empty: %v", err)
	}
	if _, err := os.Stat(VersionsDir(bookDir)); err != nil {
		t.Errorf("versions dir removed when not empty")
	}

	// Remove the slot; dir now empty → prune removes it.
	_ = os.RemoveAll(slot)
	if err := PruneEmptyVersionsDir(bookDir); err != nil {
		t.Fatalf("prune empty: %v", err)
	}
	if _, err := os.Stat(VersionsDir(bookDir)); !os.IsNotExist(err) {
		t.Errorf("versions dir should be removed")
	}

	// Missing dir is idempotent.
	if err := PruneEmptyVersionsDir(bookDir); err != nil {
		t.Errorf("prune missing dir should not error: %v", err)
	}
}
