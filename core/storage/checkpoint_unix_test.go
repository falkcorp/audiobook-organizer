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
