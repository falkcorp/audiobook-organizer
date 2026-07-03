// file: internal/database/pebble_ops_v2_closed_test.go
// version: 1.0.0
// guid: 9e4b2c17-5a6d-4f38-8c01-7d2e9f4a6b53
// last-edited: 2026-07-03

// pebble_ops_v2_closed_test.go covers the PEBBLE-CLOSED-SWEEPTICK-RESIDUAL
// defense-in-depth guard: ListWaitingDepsOps on a closed PebbleStore must
// return an error instead of propagating pebble's ErrClosed PANIC out of
// NewIter and crashing the process. This is the read the DepsScheduler sweep
// ticker performs on a periodic goroutine; a registry torn down without
// Shutdown (as internal/server test leaks can do) otherwise kills the whole
// test binary minutes later.
package database

import (
	"errors"
	"os"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	ulid "github.com/oklog/ulid/v2"
)

func TestListWaitingDepsOps_ClosedStoreReturnsError(t *testing.T) {
	tmpdir := "/tmp/test_pebble_" + ulid.Make().String()
	store, err := NewPebbleStore(tmpdir)
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer os.RemoveAll(tmpdir)
	store.WaitForWarmup()
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Pre-guard this panicked "pebble: closed" (pebble panics ErrClosed from
	// NewIter rather than returning it); post-guard it must be an error.
	rows, err := store.ListWaitingDepsOps()
	if err == nil {
		t.Fatalf("expected error from ListWaitingDepsOps on closed store, got rows=%v", rows)
	}
	if !errors.Is(err, pebble.ErrClosed) {
		t.Fatalf("expected error wrapping pebble.ErrClosed, got: %v", err)
	}
	if rows != nil {
		t.Fatalf("expected nil rows on closed store, got %v", rows)
	}
}
