// file: internal/organizer/saferename_verify_test.go
// version: 1.0.0
// guid: 9f26b4d1-70a3-4e58-8c19-2b7e05a4c396
// last-edited: 2026-08-16

// Post-move validation.
//
// A rename returning nil says the syscall was accepted. It does not say the
// file arrived where anyone wanted it. The separator bug produced 38,895
// misplaced files across 1,145 books through operations that ALL reported
// success, so "no error" is exactly the signal that failed here.

package organizer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func TestSafeRename_VerifiesTheMoveHappened(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "book", "Foundation - 01.mp3")
	dst := filepath.Join(dir, "book", "Foundation - 02.mp3")
	writeFile(t, src, 4096)

	if err := safeRename(src, dst); err != nil {
		t.Fatalf("safeRename: %v", err)
	}

	info, err := os.Lstat(dst)
	if err != nil {
		t.Fatalf("destination missing after a successful rename: %v", err)
	}
	if info.Size() != 4096 {
		t.Errorf("destination is %d bytes, want 4096", info.Size())
	}
	if _, err := os.Lstat(src); !os.IsNotExist(err) {
		t.Errorf("source still exists after the move")
	}
}

func TestSafeRename_MissingSource(t *testing.T) {
	dir := t.TempDir()
	err := safeRename(filepath.Join(dir, "nope.mp3"), filepath.Join(dir, "dst.mp3"))
	if err == nil {
		t.Fatal("safeRename accepted a source that does not exist")
	}
	if !strings.Contains(err.Error(), "stat rename source") {
		t.Errorf("error does not name the problem: %v", err)
	}
}

func TestSafeRename_RefusesExistingDestination(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.mp3")
	dst := filepath.Join(dir, "b.mp3")
	writeFile(t, src, 10)
	writeFile(t, dst, 20)

	if err := safeRename(src, dst); err == nil {
		t.Fatal("safeRename overwrote an existing destination")
	}
	// The destination must be untouched -- this guard exists to stop one
	// book's bytes replacing another's.
	if info, err := os.Lstat(dst); err != nil || info.Size() != 20 {
		t.Errorf("destination was modified by a refused rename")
	}
}

// TestVerifyRenamed_CatchesEachFailureShape exercises the validator directly,
// because the states it guards against cannot be produced through os.Rename --
// they come from a copy fallback, a filesystem quirk, or a path that turned
// out to name a directory.
func TestVerifyRenamed_CatchesEachFailureShape(t *testing.T) {
	// fakeInfo describes what the source WAS, which is what verifyRenamed
	// compares the destination against.
	regular := func(t *testing.T, size int) os.FileInfo {
		t.Helper()
		p := filepath.Join(t.TempDir(), "ref")
		writeFile(t, p, size)
		fi, err := os.Lstat(p)
		if err != nil {
			t.Fatal(err)
		}
		return fi
	}
	directory := func(t *testing.T) os.FileInfo {
		t.Helper()
		p := filepath.Join(t.TempDir(), "refdir")
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		fi, err := os.Lstat(p)
		if err != nil {
			t.Fatal(err)
		}
		return fi
	}

	t.Run("destination missing", func(t *testing.T) {
		dir := t.TempDir()
		err := verifyRenamed(filepath.Join(dir, "src"), filepath.Join(dir, "gone"), regular(t, 10))
		if err == nil || !strings.Contains(err.Error(), "unreadable") {
			t.Errorf("want an unreadable-destination error, got %v", err)
		}
	})

	t.Run("destination is a directory", func(t *testing.T) {
		dir := t.TempDir()
		dst := filepath.Join(dir, "Foundation - 01")
		if err := os.MkdirAll(dst, 0o755); err != nil {
			t.Fatal(err)
		}
		err := verifyRenamed(filepath.Join(dir, "src"), dst, regular(t, 10))
		if err == nil || !strings.Contains(err.Error(), "destination") {
			t.Errorf("want a kind-mismatch error, got %v", err)
		}
	})

	t.Run("directory move is allowed", func(t *testing.T) {
		// ReOrganizeInPlace renames whole book folders through safeRename, so
		// a directory destination is correct when the source was a directory.
		dir := t.TempDir()
		dst := filepath.Join(dir, "Author", "Title")
		if err := os.MkdirAll(dst, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := verifyRenamed(filepath.Join(dir, "src"), dst, directory(t)); err != nil {
			t.Errorf("a legitimate directory move was rejected: %v", err)
		}
	})

	t.Run("size changed", func(t *testing.T) {
		dir := t.TempDir()
		dst := filepath.Join(dir, "dst.mp3")
		writeFile(t, dst, 5)
		err := verifyRenamed(filepath.Join(dir, "src"), dst, regular(t, 4096))
		if err == nil || !strings.Contains(err.Error(), "expected 4096") {
			t.Errorf("want a size mismatch error, got %v", err)
		}
	})

	t.Run("source survived: the move became a copy", func(t *testing.T) {
		dir := t.TempDir()
		src := filepath.Join(dir, "src.mp3")
		dst := filepath.Join(dir, "dst.mp3")
		writeFile(t, src, 10)
		writeFile(t, dst, 10)
		err := verifyRenamed(src, dst, regular(t, 10))
		if err == nil || !strings.Contains(err.Error(), "became a copy") {
			t.Errorf("want a leftover-source error, got %v", err)
		}
	})

	t.Run("clean move passes", func(t *testing.T) {
		dir := t.TempDir()
		dst := filepath.Join(dir, "dst.mp3")
		writeFile(t, dst, 10)
		if err := verifyRenamed(filepath.Join(dir, "src.mp3"), dst, regular(t, 10)); err != nil {
			t.Errorf("a correct move was rejected: %v", err)
		}
	})
}
