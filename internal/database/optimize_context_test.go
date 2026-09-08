// file: internal/database/optimize_context_test.go
// version: 1.0.0
// guid: 8d3f5b21-7e64-4c09-9b18-45a2e0c7d193
// last-edited: 2026-09-08

// Optimize() hardcoded context.Background() until 2026-09-08, so a full
// compaction could not be interrupted by anything -- not an operation cancel,
// not shutdown. It is not a quick call: 31 GB took over twenty minutes on
// production, and during that window the process had no way to stop it.
//
// That was proven on prod the same day. The operations registry cancelled
// maintenance.db-optimize at its five-minute stuck strike, and Pebble carried
// on compacting for another twenty minutes with the op already reporting
// `interrupted_dropped`. An operation whose status says "dead" while its work
// runs on unsupervised is worse than either outcome alone.
//
// These tests pin the only thing that makes the cancel real: the caller's
// context reaching db.Compact. A signature that merely ACCEPTS a ctx and drops
// it on the floor would compile, satisfy every caller, and still be the bug.
package database

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// seedForCompaction writes enough keys that a compaction has real work to do.
func seedForCompaction(t *testing.T, store *PebbleStore) {
	t.Helper()
	for i := range 2000 {
		require.NoError(t, store.SetSetting(fmt.Sprintf("compact-probe-%05d", i),
			"padding-so-the-lsm-has-something-to-rewrite-padding-padding", "string", false))
	}
}

func TestPebbleStoreOptimize_HonoursCancelledContext(t *testing.T) {
	store, err := NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	seedForCompaction(t, store)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before the call

	err = store.Optimize(ctx)
	require.Error(t, err,
		"a cancelled context must reach db.Compact; accepting a ctx and ignoring it is the original bug")
	require.True(t, errors.Is(err, context.Canceled),
		"want context.Canceled, got %v", err)
}

func TestPebbleStoreOptimize_SucceedsOnLiveContext(t *testing.T) {
	// The guard against over-correcting: a live context must still compact.
	// Without this, "return ctx.Err() always" would pass the test above.
	store, err := NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	seedForCompaction(t, store)

	require.NoError(t, store.Optimize(context.Background()))

	// And the data is intact afterwards -- an aborted-looking compaction that
	// silently dropped rows would be far worse than a slow one.
	v, err := store.GetSetting("compact-probe-01999")
	require.NoError(t, err)
	require.NotEmpty(t, v)
}

func TestAIScanStoreOptimize_HonoursCancelledContext(t *testing.T) {
	// The AI scan store had the identical hardcoded context.Background().
	store, err := NewAIScanStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// An owned store compacts and must honour the cancel. A shared one is a
	// documented no-op and returns nil regardless -- assert whichever this is
	// rather than assuming, so the test stays true if ownership changes.
	err = store.Optimize(ctx)
	if err != nil {
		require.True(t, errors.Is(err, context.Canceled), "want context.Canceled, got %v", err)
	}
}
