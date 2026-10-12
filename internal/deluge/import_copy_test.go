// file: internal/deluge/import_copy_test.go
// version: 1.0.0
// guid: 0f4a7c19-6d82-4b35-9e61-a3c58d07b2f4
// last-edited: 2026-10-11

package deluge

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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
