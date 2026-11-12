// file: core/kernel/restore_archive_files_unix_test.go
// version: 1.0.0
// guid: 5b9e2d83-1c64-4a07-8f3a-d02e76b1c495
// last-edited: 2026-11-12

//go:build unix

package kernel

import (
	"bytes"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"
)

// probeFS is the real filesystem seen through a wrapper (it unwraps to
// vfs.Default, as errorfs does), recording the mode of a file at the moment the
// code under test gets its writable handle: a file that is private only after a
// later chmod shows its wider mode here.
type probeFS struct {
	vfs.FS
	modes []os.FileMode
}

func (p *probeFS) Unwrap() vfs.FS { return p.FS }

func (p *probeFS) record(name string) {
	if fi, err := os.Stat(name); err == nil {
		p.modes = append(p.modes, fi.Mode().Perm())
	}
}

func (p *probeFS) Create(name string, c vfs.DiskWriteCategory) (vfs.File, error) {
	f, err := p.FS.Create(name, c)
	if err == nil {
		p.record(name)
	}
	return f, err
}

func (p *probeFS) OpenReadWrite(name string, c vfs.DiskWriteCategory, opts ...vfs.OpenOption) (vfs.File, error) {
	f, err := p.FS.OpenReadWrite(name, c, opts...)
	if err == nil {
		p.record(name)
	}
	return f, err
}

// The operator's keyring copy holds the secrets key. It must be 0600 when its
// first byte is written, not after: with the umask cleared a file made through
// vfs.FS.Create is 0666, and a process that opens it before the chmod keeps a
// handle that reads the key written next.
func TestCopyOperatorFileIsPrivateBeforeItsFirstByte(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)

	dir := t.TempDir()
	src := filepath.Join(dir, "secrets.keyring")
	want := []byte("batcave-secrets-key-material")
	if err := os.WriteFile(src, want, 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "staged")
	probe := &probeFS{FS: vfs.Default}
	if err := copyOperatorFile(probe, src, dst, 1<<20, true); err != nil {
		t.Fatal(err)
	}
	if len(probe.modes) == 0 {
		t.Fatal("the probe saw no writable handle: this test checks nothing")
	}
	for _, m := range probe.modes {
		if m != 0o600 {
			t.Fatalf("the staged copy was %04o when it was opened for writing, want 0600", m)
		}
	}
	fi, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("the staged copy ended %04o, want 0600", fi.Mode().Perm())
	}
	got, err := os.ReadFile(dst)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("the staged copy holds %q (%v), want %q", got, err, want)
	}
}

// A file that was already there, wider than 0600, is made private before the key
// is written into it.
func TestCopyOperatorFileNarrowsAFileThatWasAlreadyThere(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)

	dir := t.TempDir()
	src := filepath.Join(dir, "secrets.keyring")
	if err := os.WriteFile(src, []byte("alfred-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "staged")
	if err := os.WriteFile(dst, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := copyOperatorFile(&probeFS{FS: vfs.Default}, src, dst, 1<<20, true); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("a pre-existing 0644 file ended %04o, want 0600", fi.Mode().Perm())
	}
	if b, _ := os.ReadFile(dst); string(b) != "alfred-key" {
		t.Fatalf("the copy holds %q", b)
	}
}
