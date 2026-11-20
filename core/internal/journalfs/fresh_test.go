// file: core/internal/journalfs/fresh_test.go
// version: 1.0.0
// guid: b3e70c58-1d94-4a2f-8c6b-5f0a9d2e7143
// last-edited: 2026-11-19

package journalfs_test

import (
	"os"
	"testing"

	"github.com/falkcorp/mediacore/internal/bootseam/bootseamtest"
)

// TestMemFS_RenamedEntryStaysUnsyncedUntilTheDirectorySync: a rename's new
// entry is durable at the directory's fsync and not before, and a later
// truncation's file Sync must not make it so: it is the same rule as for a
// created file. After a durable file a is renamed to b without a directory
// fsync, truncating and syncing b leaves a, with its old contents, the durable
// file, and no b.
func TestMemFS_RenamedEntryStaysUnsyncedUntilTheDirectorySync(t *testing.T) {
	e := newEnv(t, newStore(t), bootseamtest.Options{Clones: true})
	a, b := stateDir+"/a", stateDir+"/b"
	f, err := e.fsys.Open(a, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("abcdef")); err != nil {
		t.Fatal(err)
	}
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := e.fsys.SyncDir(stateDir); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := e.fsys.Rename(a, b); err != nil {
		t.Fatal(err)
	}
	g, err := e.fsys.Open(b, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Truncate(2); err != nil {
		t.Fatal(err)
	}
	if err := g.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	settled := e.rec.Settled().FS
	if exists(t, settled, b) {
		t.Error("a crash finds the renamed file: the truncation's Sync made a rename durable that no directory fsync had")
	}
	raw, err := readAll(settled, a)
	if err != nil || string(raw) != "abcdef" {
		t.Errorf("the durable file a = %q, %v; want its old contents %q", raw, err, "abcdef")
	}
}
