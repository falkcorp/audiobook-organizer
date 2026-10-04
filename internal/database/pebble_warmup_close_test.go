// file: internal/database/pebble_warmup_close_test.go
// version: 1.0.2
// guid: 6b3a1e9c-2f47-4d80-9a15-7c0e8b2d4f63
// last-edited: 2026-10-03

package database

import (
	"fmt"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
)

// seedPebbleDir writes enough book:/author:/series: keys directly (no warmup)
// that a subsequent NewPebbleStore's async memdb warmup takes long enough to
// still be iterating when Close is called.
//
// fs is the filesystem both the seed and the store open; the test passes one
// vfs.NewMem per iteration so the reopen sees the seeded keys without paying an
// fsync for them.
func seedPebbleDir(t *testing.T, fs vfs.FS, path string, n int) {
	t.Helper()
	db, err := pebble.Open(path, &pebble.Options{FS: fs})
	if err != nil {
		t.Fatalf("seed open: %v", err)
	}
	batch := db.NewBatch()
	for i := range n {
		for _, prefix := range []string{"book:", "author:", "series:"} {
			key := fmt.Sprintf("%s%06d", prefix, i)
			// Minimal valid JSON; warmup unmarshal failures are skipped, but the
			// iterator still walks every key (where the panic used to happen).
			_ = batch.Set([]byte(key), []byte(`{"id":"x"}`), nil)
		}
	}
	if err := db.Apply(batch, pebble.Sync); err != nil {
		t.Fatalf("seed apply: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("seed close: %v", err)
	}
}

// TestPebbleStore_CloseDuringWarmupDoesNotPanic is the regression test for the
// "pebble: closed" panic that made the database package crash under the normal
// (non -race) test run. NewPebbleStore launches an async memdb warmup goroutine
// that iterates the DB; Close() closed the DB out from under it, so warmIter's
// db.NewIter panicked. Close must now stop and wait for the warmup goroutine
// before closing the DB.
//
// Repeated open+immediate-close over a seeded DB reliably triggered the panic
// pre-fix; post-fix every iteration is clean.
//
// Each iteration seeds and reopens the database on its own in-memory
// filesystem (newPebbleStore with a vfs.NewMem), which runs the same
// constructor and warmup goroutine as NewPebbleStore without an fsync per
// write. The race is between Close and the warmup goroutine, not the disk.
func TestPebbleStore_CloseDuringWarmupDoesNotPanic(t *testing.T) {
	for i := range 40 {
		fs := vfs.NewMem()
		dir := fmt.Sprintf("db-%d", i)
		seedPebbleDir(t, fs, dir, 6000)

		store, err := newPebbleStore(dir, fs)
		if err != nil {
			t.Fatalf("iter %d: NewPebbleStore: %v", i, err)
		}
		// Close immediately — the warmup goroutine is mid-iteration. Must not panic.
		if err := store.Close(); err != nil {
			t.Fatalf("iter %d: Close: %v", i, err)
		}
	}
}
