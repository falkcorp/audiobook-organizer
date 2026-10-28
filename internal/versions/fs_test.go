// file: internal/versions/fs_test.go
// version: 1.1.1
// guid: 4e2c8a1d-5b9f-4f70-a7c6-2d8e0f1b9a57
// last-edited: 2026-10-28

package versions

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
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

	newPaths, _, errs := MoveToVersionsDir(bookDir, "ver1", []string{orig})
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

	newPaths, _, errs := MoveToVersionsDir(bookDir, "ver-multi", srcs)
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

// A source that is gone is an error, never "already moved": with the file the
// first call moved sitting at the destination, the second call must not take it
// for its own. (It used to, and so adopted any file that carried the name.)
func TestMoveToVersionsDir_RepeatIsRefusedNotIdempotent(t *testing.T) {
	bookDir := t.TempDir()
	orig := filepath.Join(bookDir, "Year One.m4b")
	writeFile(t, orig, "audio")

	_, renamed1, errs1 := MoveToVersionsDir(bookDir, "ver1", []string{orig})
	if len(errs1) > 0 || len(renamed1) != 1 || !renamed1[0] {
		t.Fatalf("first move: renamed=%v errs=%v", renamed1, errs1)
	}
	_, renamed2, errs2 := MoveToVersionsDir(bookDir, "ver1", []string{orig})
	if len(errs2) != 1 {
		t.Fatalf("repeat errs = %v, want the missing source refused", errs2)
	}
	if len(renamed2) != 1 || renamed2[0] {
		t.Errorf("repeat renamed = %v, want [false]", renamed2)
	}
	if got := fileContent(t, filepath.Join(VersionSlotDir(bookDir, "ver1"), "Year One.m4b")); got != "audio" {
		t.Errorf("the moved file changed: %q", got)
	}
}

// A file with the source's name at the destination, and no source: the old
// "already moved" branch. It is an error, and the stranger is not touched.
func TestMoveFromVersionsDir_StrayAtDestinationIsNotAdopted(t *testing.T) {
	bookDir := t.TempDir()
	stray := filepath.Join(bookDir, "Hush.m4b")
	writeFile(t, stray, "another book")
	missing := filepath.Join(VersionSlotDir(bookDir, "ver1"), "Hush.m4b")
	if err := os.MkdirAll(filepath.Dir(missing), 0o775); err != nil {
		t.Fatal(err)
	}

	newPaths, renamed, errs := MoveFromVersionsDir(bookDir, "ver1", []string{missing})
	if len(errs) != 1 {
		t.Fatalf("errs = %v, want the missing source refused", errs)
	}
	if renamed[0] {
		t.Error("a move that did not happen is reported as renamed")
	}
	if newPaths[0] != stray {
		t.Errorf("new path = %s, want %s", newPaths[0], stray)
	}
	if got := fileContent(t, stray); got != "another book" {
		t.Errorf("the stranger's file changed: %q", got)
	}
}

// Moves report exactly what they renamed: a file that fails is false, and the
// files around it are still tried and reported on their own.
func TestMoveToVersionsDir_ReportsWhatItRenamed(t *testing.T) {
	bookDir := t.TempDir()
	first := filepath.Join(bookDir, "Knightfall 1.mp3")
	third := filepath.Join(bookDir, "Knightfall 3.mp3")
	writeFile(t, first, "1")
	writeFile(t, third, "3")
	missing := filepath.Join(bookDir, "Knightfall 2.mp3")

	_, renamed, errs := MoveToVersionsDir(bookDir, "ver1", []string{first, missing, third})
	if len(errs) != 1 {
		t.Fatalf("errs = %v, want exactly the missing file", errs)
	}
	if want := []bool{true, false, true}; renamed[0] != want[0] || renamed[1] != want[1] || renamed[2] != want[2] {
		t.Errorf("renamed = %v, want %v", renamed, want)
	}
}

// renameNoReplace never replaces what is at the destination: a file, and a
// dangling link, which a Stat-based check would call free.
func TestRenameNoReplace_NeverReplaces(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	writeFile(t, src, "source")

	dstFile := filepath.Join(dir, "dst-file")
	writeFile(t, dstFile, "keep me")
	dstLink := filepath.Join(dir, "dst-link")
	if err := os.Symlink(filepath.Join(dir, "nowhere"), dstLink); err != nil {
		t.Fatal(err)
	}
	for _, rename := range []struct {
		name string
		fn   func(a, b string) error
	}{{"renameNoReplace", renameNoReplace}, {"renameChecked", renameChecked}} {
		for _, dst := range []string{dstFile, dstLink} {
			err := rename.fn(src, dst)
			if !errors.Is(err, fs.ErrExist) {
				t.Errorf("%s onto %s: err = %v, want fs.ErrExist", rename.name, filepath.Base(dst), err)
			}
			if got := fileContent(t, src); got != "source" {
				t.Errorf("%s onto %s: the source changed: %q", rename.name, filepath.Base(dst), got)
			}
		}
	}
	if got := fileContent(t, dstFile); got != "keep me" {
		t.Errorf("the destination file was replaced: %q", got)
	}
	if target, err := os.Readlink(dstLink); err != nil || target != filepath.Join(dir, "nowhere") {
		t.Errorf("the destination link changed: %q, %v", target, err)
	}

	// And onto a free path it renames.
	free := filepath.Join(dir, "free")
	if err := renameNoReplace(src, free); err != nil {
		t.Fatal(err)
	}
	if got := fileContent(t, free); got != "source" {
		t.Errorf("content = %q", got)
	}
	if _, err := os.Lstat(src); !os.IsNotExist(err) {
		t.Errorf("the source is still there: %v", err)
	}
}

func TestMoveFromVersionsDir_ReverseRoundTrip(t *testing.T) {
	bookDir := t.TempDir()
	orig := filepath.Join(bookDir, "Book.m4b")
	writeFile(t, orig, "audio-content")

	// Move into versions slot.
	newPaths, _, errs := MoveToVersionsDir(bookDir, "ver1", []string{orig})
	if len(errs) > 0 {
		t.Fatalf("move to: %v", errs)
	}

	// Move back out (simulating a swap-to-primary).
	backPaths, _, errs := MoveFromVersionsDir(bookDir, "ver1", newPaths)
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

	_, _, errs := MoveToVersionsDir(bookDir, "ver1", []string{orig})
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

// A move onto itself is refused before anything is touched: renaming a file to
// its own path would be a no-op that reports success, and a caller that took
// that for a move would believe the file had left.
func TestMovePreservingAttrs_SamePathIsRefusedAndTouchesNothing(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "Hush.m4b")
	writeFile(t, p, "hush")

	renamed, err := movePreservingAttrs(p, p)
	if err == nil || !strings.Contains(err.Error(), "same path") {
		t.Fatalf("err = %v, want the same-path refusal", err)
	}
	if renamed {
		t.Error("renamed = true for a move onto itself")
	}
	if b, rerr := os.ReadFile(p); rerr != nil || string(b) != "hush" {
		t.Errorf("file after refusal = %q, %v, want it untouched", b, rerr)
	}
	// Refused before the parent was created or anything renamed: the folder
	// holds only the file.
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("folder has %d entries after the refusal, want 1", len(entries))
	}
}
