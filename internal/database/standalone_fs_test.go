// file: internal/database/standalone_fs_test.go
// version: 1.1.0
// guid: 7a1c03ac-f1b0-4da8-9764-f22f3e7165b1
// last-edited: 2026-11-08

package database

import (
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/falkcorp/mediacore/storage"
)

// newPebbleStore is the standalone opener over fs, for this package's tests
// of a store on a filesystem they control (a fault-injecting or stalling
// wrapper, or a MemFS they reopen). nil is the OS's.
func newPebbleStore(path string, fs vfs.FS) (*PebbleStore, error) {
	o := StandaloneOptions{}
	if fs != nil {
		o.FS = storage.FSOf(fs)
	}
	return OpenStandalone(path, o)
}

// traceBatches makes every core batch built until t ends record the writes it
// stages, for stagedWrites.
func traceBatches(t testing.TB) {
	t.Helper()
	traceBatchWrites.Add(1)
	t.Cleanup(func() { traceBatchWrites.Add(-1) })
}

// stagedWrites is what b staged, in order. b must be a core batch built under
// traceBatches (a recording batch reports its recording).
func stagedWrites(t testing.TB, b *GatedBatch) []batchOp {
	t.Helper()
	switch {
	case b.rec != nil:
		return *b.rec
	case b.trace != nil:
		return *b.trace
	}
	t.Fatal("stagedWrites: the batch was not built under traceBatches")
	return nil
}

// openWithUnclaimedKeys opens a store on a fresh in-memory filesystem whose
// engine already holds kv, the way a store HEAD wrote can hold keys outside
// every books claim (an unregistered family, a neighbour of a claimed
// prefix): books can neither read nor write them under strict claims, and a
// scan must not surface them.
func openWithUnclaimedKeys(t *testing.T, kv map[string]string) *PebbleStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "unclaimed.pebble")
	for k, v := range kv {
		rawSet(t, path, nil, k, v)
	}
	s, err := NewPebbleStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	s.WaitForWarmup()
	return s
}
