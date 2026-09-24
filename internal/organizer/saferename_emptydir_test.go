// file: internal/organizer/saferename_emptydir_test.go
// version: 1.0.0
// guid: e12d01fe-5747-4d06-98b6-ac7563d9621b
// last-edited: 2026-09-24

// Directory moves onto an existing destination.
//
// The organize caller treats an empty destination folder as free, but until
// 2026-09-24 moveExclusive sent directories through safeRename, which refuses
// every existing dst. 101 auto-organize failures on 2026-09-23 were this, and
// 92 of those destinations were empty folders.

package organizer

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func TestMoveExclusive_DirOntoEmptyDirSucceeds(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	writeFile(t, filepath.Join(src, "a.mp3"), 10)
	writeFile(t, filepath.Join(src, "b.mp3"), 20)
	if err := os.Mkdir(dst, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := moveExclusive(src, dst); err != nil {
		t.Fatalf("moveExclusive onto empty dir: %v", err)
	}
	if _, err := os.Lstat(src); !os.IsNotExist(err) {
		t.Fatalf("source still present after move: %v", err)
	}
	for name, size := range map[string]int64{"a.mp3": 10, "b.mp3": 20} {
		fi, err := os.Stat(filepath.Join(dst, name))
		if err != nil || fi.Size() != size {
			t.Fatalf("%s not moved intact: fi=%v err=%v", name, fi, err)
		}
	}
}

func TestMoveExclusive_DirOntoNonEmptyDirRefused(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	writeFile(t, filepath.Join(src, "a.mp3"), 10)
	writeFile(t, filepath.Join(dst, "occupant.mp3"), 30)

	err := moveExclusive(src, dst)
	if !errors.Is(err, fs.ErrExist) {
		t.Fatalf("want fs.ErrExist, got %v", err)
	}
	assertSize(t, filepath.Join(src, "a.mp3"), 10)
	assertSize(t, filepath.Join(dst, "occupant.mp3"), 30)
}

func TestMoveExclusive_DirOntoFileRefused(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	writeFile(t, filepath.Join(src, "a.mp3"), 10)
	writeFile(t, dst, 5)

	err := moveExclusive(src, dst)
	if !errors.Is(err, fs.ErrExist) {
		t.Fatalf("want fs.ErrExist, got %v", err)
	}
	assertSize(t, filepath.Join(src, "a.mp3"), 10)
	assertSize(t, dst, 5)
}

// A file landing in the empty destination between the emptiness check and
// the rename must make the kernel refuse, never lose the late file.
func TestMoveExclusive_DirOntoDirFilledAfterCheckRefused(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	writeFile(t, filepath.Join(src, "a.mp3"), 10)
	if err := os.Mkdir(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	late := filepath.Join(dst, "late.mp3")
	beforeEmptyDirRename = func(string) { writeFile(t, late, 7) }
	t.Cleanup(func() { beforeEmptyDirRename = nil })

	err := moveExclusive(src, dst)
	if !errors.Is(err, fs.ErrExist) {
		t.Fatalf("want fs.ErrExist, got %v", err)
	}
	assertSize(t, late, 7)
	assertSize(t, filepath.Join(src, "a.mp3"), 10)
}

func TestMoveExclusive_DirOntoAbsentDstUnchanged(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "new", "dst")
	writeFile(t, filepath.Join(src, "a.mp3"), 10)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := moveExclusive(src, dst); err != nil {
		t.Fatalf("moveExclusive to absent dst: %v", err)
	}
	assertSize(t, filepath.Join(dst, "a.mp3"), 10)
}

func assertSize(t *testing.T, path string, want int64) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	if fi.Size() != want {
		t.Fatalf("%s is %d bytes, want %d", path, fi.Size(), want)
	}
}

// End to end through organizeBooks: a directory book whose computed target is
// an existing EMPTY folder is moved into it. Before 2026-09-24 this counted as
// Failed with "destination already exists".
func TestOrganize_DirectoryBookOntoEmptyTargetDir(t *testing.T) {
	svc, store, root := setupInPlace(t)
	dir := filepath.Join(root, "incoming", "Dir Book")
	b := addInPlaceBook(t, store, "dirbook-1", "Dir Book", filepath.Join(dir, "part 01.mp3"), filled(3000, 0x51), nil, 0)
	writeFile(t, filepath.Join(dir, "part 02.mp3"), 3100)
	if err := store.CreateBookFile(&database.BookFile{ID: "dirbook-1-f2", BookID: b.ID, FilePath: filepath.Join(dir, "part 02.mp3"), FileSize: 3100}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ModifyBook(b.ID, func(bk *database.Book) error { bk.FilePath = dir; return nil }); err != nil {
		t.Fatal(err)
	}
	b.FilePath = dir

	target, err := svc.newOrganizer().GenerateTargetDirPath(b)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(target, 0o775); err != nil {
		t.Fatal(err)
	}

	stats := svc.organizeBooks(context.Background(), []database.Book{*b}, nil, &noopLogger{}, "")
	if stats.Failed != 0 || stats.Skipped != 0 {
		t.Fatalf("directory book onto an empty target must organize, got %+v", stats)
	}
	assertSize(t, filepath.Join(target, "part 01.mp3"), 3000)
	assertSize(t, filepath.Join(target, "part 02.mp3"), 3100)
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatalf("source dir still present: %v", err)
	}
	var files []database.BookFile
	files, err = store.GetBookFiles(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if filepath.Dir(f.FilePath) != target {
			t.Fatalf("book_file %s not repointed: %s", f.ID, f.FilePath)
		}
	}
}
