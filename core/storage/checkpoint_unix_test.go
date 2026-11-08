// file: core/storage/checkpoint_unix_test.go
// version: 1.0.0
// guid: 8c005d96-229c-4e91-ae0d-2e997b1d71f9
// last-edited: 2026-11-07

//go:build unix

package storage

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"
)

// TestCheckpointModes (05-storage §5S.10, SEC-9; creation half): on the real
// filesystem under a permissive umask, .mediacore-checkpoints/ and every
// directory of the copy are 0700 and every file 0600, the record and the
// .migration-checkpoint pointer included. The sweep half is R1-24's. Not
// parallel: the umask is process-wide.
func TestCheckpointModes(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)
	e := headStore(t, vfs.Default, cpOwners()...)
	db := e.open()
	dir := cpDir(e)
	check := func(p string, want os.FileMode) {
		t.Helper()
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s: mode %o, want %o", filepath.Base(p), got, want)
		}
	}
	check(dir, 0o700)
	check(filepath.Join(dir, db.CheckpointName()+".json"), 0o600)
	check(e.path+".migration-checkpoint", 0o600)
	n := 0
	err := filepath.WalkDir(filepath.Join(dir, db.CheckpointName()), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		n++
		if d.IsDir() {
			check(p, 0o700)
		} else {
			check(p, 0o600)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n < 4 {
		t.Fatalf("walked %d entries; the copy looks empty", n)
	}
}

// wrappedOS is a pass-through vfs over the real filesystem that unwraps to
// it, as a fault-injecting vfs does.
type wrappedOS struct{ vfs.FS }

func (w wrappedOS) Unwrap() vfs.FS { return w.FS }

// TestOSChecksSeeThroughWrappers (review MINOR-5): the generation, device
// and chmod helpers compare vfs.Root(fs) with vfs.Default, so a wrapper over
// the real filesystem (R1-21's fault injector) still gets real modes, device
// checks and directory identities; only a non-OS filesystem is exempt.
func TestOSChecksSeeThroughWrappers(t *testing.T) {
	osfs := wrappedOS{FS: vfs.Default}
	dir := t.TempDir()
	file := filepath.Join(dir, "batsignal")
	if err := os.WriteFile(file, []byte("lit"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := osChmod(osfs)(file, false); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(file); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode through a wrapper = %v (%v), want 0600", fi.Mode().Perm(), err)
	}
	if _, ino, err := osDirIdentity(osfs)(dir); err != nil || ino == 0 {
		t.Fatalf("directory identity through a wrapper = inode %d (%v), want the real inode", ino, err)
	}
	if _, err := osSameDevice(osfs)(dir, filepath.Join(dir, "missing")); err == nil {
		t.Fatal("the device check through a wrapper did not stat the paths")
	}
	// A non-OS filesystem stays exempt.
	mem := vfs.NewMem()
	if _, ino, err := osDirIdentity(mem)(dir); err != nil || ino != 0 {
		t.Fatalf("directory identity on MemFS = %d (%v), want the constant", ino, err)
	}
}
