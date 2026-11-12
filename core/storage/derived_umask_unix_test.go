// file: core/storage/derived_umask_unix_test.go
// version: 1.0.0
// guid: 2a7c7258-6294-4d68-92ed-9425829dc5c2
// last-edited: 2026-11-12

//go:build unix

package storage

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestCreateOwnerOnlyIsOwnerOnlyFromTheStart: the temp file of a marker is
// created 0600 by the OS, not created with the umask's default and chmod-ed
// later, so it is never readable by others, not for an instant. Under umask 0 a
// plain create is 0666: only a creation mode of 0600 passes. A leftover of a
// crashed write that was 0644 is replaced, because a mode applies only to a file
// that is created.
func TestCreateOwnerOnlyIsOwnerOnlyFromTheStart(t *testing.T) {
	old := syscall.Umask(0) // process-wide: restored below; this test does not run in parallel
	defer syscall.Umask(old)
	dir := t.TempDir()
	for name, leftover := range map[string]bool{"fresh.tmp": false, "crashed.tmp": true} {
		path := filepath.Join(dir, name)
		if leftover {
			if err := os.WriteFile(path, []byte("left by a crash"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		f, err := createOwnerOnly(path)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path) // before a byte is written
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Errorf("%s: mode %o as soon as it exists, want 600", name, mode)
		}
		if info.Size() != 0 {
			t.Errorf("%s: a leftover's content survived: %d bytes", name, info.Size())
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	// The whole writer under umask 0: the marker is 0600 and no temp file is left.
	path := filepath.Join(dir, "signal.bleve.mapping")
	if err := WriteFileDurable(path, []byte("v1\n")); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("marker under umask 0: %v %v", info, err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temp file left behind: %v", err)
	}
}
