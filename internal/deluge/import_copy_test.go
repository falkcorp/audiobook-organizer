// file: internal/deluge/import_copy_test.go
// version: 1.1.0
// guid: 0f4a7c19-6d82-4b35-9e61-a3c58d07b2f4
// last-edited: 2026-10-11

package deluge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// CopyIntoLibrary is the importers' one copy step and never overwrites.
func TestCopyIntoLibrary(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.m4b")
	if err := os.WriteFile(src, []byte("audio data"), 0o644); err != nil {
		t.Fatal(err)
	}
	read := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	t.Run("new destination is created", func(t *testing.T) {
		dest := filepath.Join(dir, "new.m4b")
		created, err := CopyIntoLibrary(src, dest)
		if err != nil || !created || read(dest) != "audio data" {
			t.Errorf("got created=%v err=%v content=%q, want a created copy", created, err, read(dest))
		}
	})
	t.Run("a different file at the destination is refused and left alone", func(t *testing.T) {
		dest := filepath.Join(dir, "other.m4b")
		if err := os.WriteFile(dest, []byte("another book!"), 0o644); err != nil {
			t.Fatal(err)
		}
		created, err := CopyIntoLibrary(src, dest)
		if err == nil || created {
			t.Fatalf("got created=%v err=%v, want a refusal", created, err)
		}
		if got := read(dest); got != "another book!" {
			t.Errorf("destination now holds %q: it was overwritten", got)
		}
	})
	// Same size, different bytes: the size pre-check cannot tell these apart,
	// so this is the case that proves the content is compared. A sameContent
	// that answered from the sizes alone adopted another book's file here.
	t.Run("same size, different bytes: refused and left alone", func(t *testing.T) {
		dest := filepath.Join(dir, "samesize.m4b")
		if err := os.WriteFile(dest, []byte("audio datA"), 0o644); err != nil {
			t.Fatal(err)
		}
		created, err := CopyIntoLibrary(src, dest)
		if err == nil || created {
			t.Fatalf("got created=%v err=%v, want a refusal", created, err)
		}
		if !strings.Contains(err.Error(), "remove it and import again") {
			t.Errorf("err = %v, want it to say how to clear a partial copy", err)
		}
		if got := read(dest); got != "audio datA" {
			t.Errorf("destination now holds %q: it was overwritten", got)
		}
	})
	// A symlink at the destination is never adopted, even one pointing at
	// the source itself (whose bytes trivially match): the row would name a
	// link into the seeding directory, not a library copy.
	t.Run("a symlink at the destination is refused, even one to the source", func(t *testing.T) {
		dest := filepath.Join(dir, "link.m4b")
		if err := os.Symlink(src, dest); err != nil {
			t.Fatal(err)
		}
		created, err := CopyIntoLibrary(src, dest)
		if err == nil || created {
			t.Fatalf("got created=%v err=%v, want a refusal", created, err)
		}
		if info, lerr := os.Lstat(dest); lerr != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("the symlink was replaced (lstat %v, %v)", info, lerr)
		}
	})
	t.Run("an identical file at the destination is adopted, not rewritten", func(t *testing.T) {
		dest := filepath.Join(dir, "same.m4b")
		if err := os.WriteFile(dest, []byte("audio data"), 0o644); err != nil {
			t.Fatal(err)
		}
		old := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
		if err := os.Chtimes(dest, old, old); err != nil {
			t.Fatal(err)
		}
		created, err := CopyIntoLibrary(src, dest)
		if err != nil || created {
			t.Fatalf("got created=%v err=%v, want adopted (created=false) and no error", created, err)
		}
		if info, _ := os.Stat(dest); !info.ModTime().Equal(old) {
			t.Error("an adopted destination was rewritten")
		}
	})
	t.Run("a missing source is an error", func(t *testing.T) {
		dest := filepath.Join(dir, "missing.m4b")
		if _, err := CopyIntoLibrary(filepath.Join(dir, "nope.m4b"), dest); err == nil {
			t.Error("want an error for a missing source")
		}
		if _, err := os.Stat(dest); err == nil {
			t.Error("a destination was left behind")
		}
	})
}

// A typed-nil checker in ImportOptions (a caller's unset *ProtectedPathCache
// field) is no checker: ImportToLibraryWith must not call a method on its nil
// receiver, on the already-imported path (isProtected) or the static check.
func TestImportToLibraryWith_TypedNilProtected(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(t.TempDir(), "book.m4b")
	if err := os.WriteFile(src, []byte("audio data"), 0o644); err != nil {
		t.Fatal(err)
	}
	var unset *ProtectedPathCache
	cfg := &config.Config{RootDir: root}

	t.Run("already imported", func(t *testing.T) {
		now := time.Now()
		bf := &database.BookFile{ID: "bf-1", BookID: "b-1", FilePath: src, ImportedFromDelugeAt: &now}
		got, err := ImportToLibraryWith(cfg, nil, &fakeDelugStore{}, bf, ImportOptions{Protected: unset})
		if err != nil || got != src {
			t.Fatalf("got %q, %v; want %q, nil", got, err, src)
		}
	})
	t.Run("pending", func(t *testing.T) {
		bf := &database.BookFile{ID: "bf-2", BookID: "b-1", FilePath: src}
		got, err := ImportToLibraryWith(cfg, nil, &fakeDelugStore{}, bf, ImportOptions{Protected: unset})
		if want := filepath.Join(root, "book.m4b"); err != nil || got != want {
			t.Fatalf("got %q, %v; want %q, nil", got, err, want)
		}
	})
}
