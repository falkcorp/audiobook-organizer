// file: core/storage/derived_durable_test.go
// version: 1.0.1
// guid: 27f5817e-8309-4fcc-bede-df7c2b06f3c6
// last-edited: 2026-11-11

package storage

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"
)

func readAll(t *testing.T, fsys vfs.FS, path string) (string, bool) {
	t.Helper()
	f, err := fsys.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(b), true
}

// TestWriteFileDurableSurvivesACrash: a marker or stamp that was written is
// there, whole, after a crash that keeps only synced state. Skipping the file's
// fsync loses the content, skipping the directory's loses the rename, so a
// replaced marker would come back as the old one.
func TestWriteFileDurableSurvivesACrash(t *testing.T) {
	mem := vfs.NewCrashableMem()
	if err := mem.MkdirAll("/batcave/index/gotham", 0o750); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"/", "/batcave", "/batcave/index", "/batcave/index/gotham"} {
		f, err := mem.OpenDir(d) // the directories themselves are durable before the test starts
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Sync(); err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
	}
	path := "/batcave/index/gotham/signal.bleve.rebuilding"
	if err := writeFileDurable(mem, path, []byte("old\n"), nil); err != nil {
		t.Fatal(err)
	}
	if err := writeFileDurable(mem, path, []byte("new\n"), nil); err != nil {
		t.Fatal(err)
	}
	crashed := mem.CrashClone(vfs.CrashCloneCfg{})
	got, ok := readAll(t, crashed, path)
	if !ok || got != "new\n" {
		t.Fatalf("after a crash the marker is %q (present=%v), want the fully written new content", got, ok)
	}
	if _, ok := readAll(t, crashed, path+".tmp"); ok {
		t.Fatal("the temp file survived the rename")
	}
}

// TestWriteFileDurableLeavesNothingOnFailure: a failed write removes its temp
// file and never touches the target.
func TestWriteFileDurableLeavesNothingOnFailure(t *testing.T) {
	mem := vfs.NewMem()
	if err := writeFileDurable(mem, "/nodir/marker", []byte("x"), nil); err == nil {
		t.Fatal("a write into a missing directory succeeded")
	}
	if _, ok := readAll(t, mem, "/nodir/marker.tmp"); ok {
		t.Fatal("a failed write left its temp file")
	}
}

// TestRemoveFileDurableSurvivesACrash: a removed marker stays removed after a
// crash that keeps only synced state; without the directory sync it comes back.
func TestRemoveFileDurableSurvivesACrash(t *testing.T) {
	mem := vfs.NewCrashableMem()
	if err := mem.MkdirAll("/batcave/index", 0o750); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"/", "/batcave", "/batcave/index"} {
		f, err := mem.OpenDir(d)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Sync(); err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
	}
	path := "/batcave/index/signal.bleve.rebuilding"
	if err := writeFileDurable(mem, path, []byte("1\n"), nil); err != nil {
		t.Fatal(err)
	}
	if err := removeFileDurable(mem, path); err != nil {
		t.Fatal(err)
	}
	crashed := mem.CrashClone(vfs.CrashCloneCfg{})
	if got, ok := readAll(t, crashed, path); ok {
		t.Fatalf("the removed marker came back after a crash: %q", got)
	}
	if err := removeFileDurable(mem, path); err != nil {
		t.Fatalf("removing an absent file: %v", err)
	}
}

// TestWriteFileDurableIsOwnerOnly: markers and stamps are 0600, as the plain
// os.WriteFile calls they replaced were, whatever the umask or a leftover temp
// file's mode.
func TestWriteFileDurableIsOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "signal.bleve.mapping")
	if err := os.WriteFile(path+".tmp", []byte("left by a crash"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileDurable(path, []byte("v1\n")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("marker mode = %o, want 600", mode)
	}
	if err := RemoveFileDurable(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the removed marker: %v", err)
	}
}
