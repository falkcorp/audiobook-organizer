// file: internal/books/store/pebble_store_ops_v2_hollow_test.go
// version: 1.1.1
// guid: 6b1f4c2e-9d3a-4e7b-8c5f-2a0d9e7b3c14
// last-edited: 2026-11-09

package store

import (
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/require"
)

// The 2026-09-11 shape: a row that has been discarded (deleted) while the
// operation's goroutine is still alive and reporting. Every get-then-set writer
// must refuse the miss instead of re-creating the row as an empty shell.
func TestOpsV2WritersRefuseAMissingRow(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	p := store.(*PebbleStore)

	const ghost = "01M27QMFPFBH4JPZW0CXQ3C7J2"
	phase := "organize"
	writers := map[string]func() error{
		"IncrementResumeCountV2": func() error { return p.IncrementResumeCountV2(ghost) },
		"UpdateOpProgressV2":     func() error { return p.UpdateOpProgressV2(ghost, 3736, 3736, "done") },
		"UpdateOpPhaseV2":        func() error { return p.UpdateOpPhaseV2(ghost, &phase) },
		"UpdateOpPhaseV2(nil)":   func() error { return p.UpdateOpPhaseV2(ghost, nil) },
		"UpdateOpCheckpointV2":   func() error { return p.UpdateOpCheckpointV2(ghost, 10) },
	}
	for name, write := range writers {
		t.Run(name, func(t *testing.T) {
			err := write()
			require.Error(t, err, "a write to a row that does not exist must fail, not create it")
			require.Contains(t, err.Error(), "not found")

			_, closer, gerr := p.db.Get(opv2OpKey(ghost))
			if gerr == nil {
				closer.Close()
				t.Fatalf("%s re-created opv2:op:%s as a hollow row", name, ghost)
			}
			require.ErrorIs(t, gerr, pebble.ErrNotFound)

			rows, err := p.ListOperationsV2Since(time.Now().Add(-time.Hour), 100)
			require.NoError(t, err)
			require.Empty(t, rows, "the timeline must not grow a nameless row")
		})
	}
}

// A queued-progress write on a missing row already declined (its status guard
// never matched ""), and it must keep declining without writing.
func TestSetOpQueuedProgressV2MissingRowWritesNothing(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	p := store.(*PebbleStore)

	written, err := p.SetOpQueuedProgressV2("01M27QMFPFBH4JPZW0CXQ3C7J2", 1, 2, "x")
	require.NoError(t, err)
	require.False(t, written)
	rows, err := p.ListOperationsV2Since(time.Now().Add(-time.Hour), 100)
	require.NoError(t, err)
	require.Empty(t, rows)
}
