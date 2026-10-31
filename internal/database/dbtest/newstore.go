// file: internal/database/dbtest/newstore.go
// version: 1.1.0
// guid: 5e8c1a27-3f94-4b60-a1d7-9c2e6b4f0a83
// last-edited: 2026-10-31

package dbtest

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// warmupTimeout bounds the wait for memdb to serve. An empty fixture store
// warms in milliseconds; this is generous enough for a CI agent under heavy
// load and still turns a wedged warmup into a failure instead of a hang.
const warmupTimeout = 2 * time.Minute

type options struct {
	onDisk       bool
	withoutMemDB bool
}

// Option adjusts how NewStore builds the store.
type Option func(*options)

// OnDisk opens a real Pebble directory under tb.TempDir() instead of the
// in-memory VFS. Use it only when the test needs bytes on a real filesystem;
// every Pebble write is a genuine fsync there.
func OnDisk() Option { return func(o *options) { o.onDisk = true } }

// WithoutMemDB turns the in-memory query layer off after construction
// (UseMemDB=false), for tests of the Pebble fallback path.
func WithoutMemDB() Option { return func(o *options) { o.withoutMemDB = true } }

// NewStore returns a ready PebbleStore for a test.
//
// It is in-memory by default (database.NewPebbleStoreInMemory), registers a
// tb.Cleanup that closes the store exactly once, and ALWAYS waits for the memdb
// warmup goroutine before returning, including with WithoutMemDB: returning
// while warmup still iterates the database risks a -race report once the test
// body or the cleanup touches it.
//
// Unless WithoutMemDB is given it also guarantees memdb is SERVING when it
// returns, and fails the test (after a bounded wait) when it is not. Waiting
// for the warmup goroutine to finish is not the same thing: a warmup that fails
// finishes too, leaving the store on its Pebble fallback, where every
// fail-closed lookup (BookFilesAtPath, the retire and combine ownership checks,
// BookFilesWithHash) refuses with "memdb is not serving". A fixture that
// silently handed such a store to a test turned a warmup problem into a
// misleading failure deep inside the test body.
//
// Do not call Close on the returned store: the cleanup owns it. It has no global state and is safe with
// t.Parallel().
func NewStore(tb testing.TB, opts ...Option) *database.PebbleStore {
	tb.Helper()
	var o options
	for _, opt := range opts {
		opt(&o)
	}

	path := filepath.Join(tb.TempDir(), "db")
	open := database.NewPebbleStoreInMemory
	if o.onDisk {
		open = database.NewPebbleStore
	}
	store, err := open(path)
	if err != nil {
		tb.Fatalf("dbtest.NewStore: open store: %v", err)
	}

	var once sync.Once
	tb.Cleanup(func() {
		once.Do(func() {
			if err := store.Close(); err != nil {
				tb.Errorf("dbtest.NewStore: close store: %v", err)
			}
		})
	})

	ctx, cancel := context.WithTimeout(context.Background(), warmupTimeout)
	defer cancel()
	if err := store.WaitForWarmupCtx(ctx); err != nil {
		tb.Fatalf("dbtest.NewStore: memdb warmup did not finish within %s: %v", warmupTimeout, err)
	}
	if o.withoutMemDB {
		store.UseMemDB = false
		return store
	}
	if !store.IsMemReady() {
		tb.Fatalf("dbtest.NewStore: warmup finished but memdb is not serving; " +
			"fail-closed lookups would refuse with \"memdb is not serving\"")
	}
	return store
}
