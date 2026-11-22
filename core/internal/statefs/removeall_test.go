// file: core/internal/statefs/removeall_test.go
// version: 0.1.0
// guid: 5d0c7f3a-8b64-4e19-a2c7-6f13d9b0e481
// last-edited: 2026-11-22

package statefs_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/cockroachdb/pebble/v2/vfs/errorfs"

	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/internal/bootseam/bootseamtest"
	"github.com/falkcorp/mediacore/internal/statefs"
)

// TestRemoveAll_Production: over the real directory RemoveAll is
// (*os.Root).RemoveAll: it removes a file, and a directory with everything
// under it; an absent name is success; a symbolic link is removed as a link
// and its target is left; a name that leaves the directory, a link out and
// the directory itself are refused.
func TestRemoveAll_Production(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	write := func(p string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("robin"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(dir, "cave", "a", "1"))
	write(filepath.Join(dir, "cave", "b"))
	write(filepath.Join(dir, "single"))
	write(filepath.Join(outside, "joker"))
	if err := os.Symlink(outside, filepath.Join(dir, "way-out")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "joker"), filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	d, err := statefs.Open(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	for _, name := range []string{"cave", "single", "absent", "cave/never/there"} {
		if err := d.RemoveAll("l", name); err != nil {
			t.Fatalf("RemoveAll(%q) = %v, want nil", name, err)
		}
		if _, err := os.Lstat(filepath.Join(dir, name)); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("RemoveAll(%q) left it: %v", name, err)
		}
	}
	if err := d.RemoveAll("l", "link"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "link")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the link survived: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "joker")); err != nil {
		t.Fatalf("RemoveAll followed a link and removed its target: %v", err)
	}
	for _, bad := range []string{"../escape", "way-out/joker", ".", "", "a/.."} {
		if err := d.RemoveAll("l", bad); err == nil {
			t.Errorf("RemoveAll(%q) succeeded", bad)
		}
	}
	if _, err := os.Stat(filepath.Join(outside, "joker")); err != nil {
		t.Fatalf("a refused name still removed %s: %v", filepath.Join(outside, "joker"), err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("the directory itself is gone: %v", err)
	}
}

// TestRemoveAll_InMemorySeam: under an in-memory seam RemoveAll removes on
// the boot's filesystem, never the real disk, with one recorded operation
// per entry (leaves before their directory, in name order), every one under
// the label it was given, so a kill or an error can strike between any two;
// an absent name records nothing; a name that leaves the directory and the
// directory itself are refused.
func TestRemoveAll_InMemorySeam(t *testing.T) {
	dir := t.TempDir() // exists on disk too: nothing may land there
	if err := os.MkdirAll(filepath.Join(dir, "cave", "a"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cave", "a", "1"), []byte("real"), 0o600); err != nil {
		t.Fatal(err)
	}
	mem := vfs.NewCrashableMem()
	for _, p := range []string{"cave/a", "cave/b"} {
		if err := mem.MkdirAll(filepath.Join(dir, p), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{"cave/a/1", "cave/a/2", "cave/c"} {
		f, err := mem.Create(filepath.Join(dir, p), vfs.WriteCategoryUnspecified)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	rec := bootseamtest.New(mem, bootseamtest.Options{})
	d, err := statefs.OpenSeams(rec.Seams(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.RemoveAll("bat.removeall", "absent"); err != nil || len(rec.Events()) != 0 {
		t.Fatalf("RemoveAll of an absent name: %v, %d operations; want success and none", err, len(rec.Events()))
	}
	for _, bad := range []string{"../x", ".", "", "/cave"} {
		if err := d.RemoveAll("bat.removeall", bad); err == nil {
			t.Errorf("RemoveAll(%q) succeeded", bad)
		}
	}
	if n := len(rec.Events()); n != 0 {
		t.Fatalf("a refused RemoveAll recorded %d operations", n)
	}
	if err := d.RemoveAll("bat.removeall", "cave"); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Stat(filepath.Join(dir, "cave")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the in-memory tree survived: %v", err)
	}
	var got []string
	for _, ev := range rec.Events() {
		if ev.Label != "bat.removeall" {
			t.Errorf("operation %v on %s carries label %q", ev.Kind, ev.Path, ev.Label)
		}
		if ev.Kind != errorfs.OpRemove {
			t.Errorf("operation %v on %s: want one Remove per entry", ev.Kind, ev.Path)
		}
		got = append(got, filepath.Base(ev.Path))
	}
	want := []string{"1", "2", "a", "b", "c", "cave"}
	if len(got) != len(want) {
		t.Fatalf("recorded removals %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("recorded removals %v, want %v", got, want)
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "cave", "a", "1")); err != nil || string(b) != "real" {
		t.Fatalf("the real file changed: %q, %v", b, err)
	}
}

// TestOpenSeams_IsOpenOverTheSeamsAlone: OpenSeams over nil seams is the real
// directory, and over a seam is that seam's filesystem, as Open reads them
// from a context; a Dir opened that way needs no context afterwards.
func TestOpenSeams_IsOpenOverTheSeamsAlone(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := statefs.OpenSeams(nil, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.RemoveAll("l", "f"); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "f")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("nil seams did not reach the real disk: %v", err)
	}
	if _, err := statefs.OpenSeams(&bootseam.Seams{FS: vfs.NewMem()}, dir); err == nil {
		t.Fatal("OpenSeams over an in-memory seam that lacks the directory succeeded")
	}
}
