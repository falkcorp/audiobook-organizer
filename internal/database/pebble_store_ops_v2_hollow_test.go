// file: internal/database/pebble_store_ops_v2_hollow_test.go
// version: 1.0.0
// guid: 6b1f4c2e-9d3a-4e7b-8c5f-2a0d9e7b3c14
// last-edited: 2026-09-11

package database

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

func seedHollowOpV2(t *testing.T, p *PebbleStore, id string) {
	t.Helper()
	// Exactly what the old UpdateOpPhaseV2(id, nil) wrote after a discard: a
	// marshalled zero OperationV2Row.
	require.NoError(t, p.pebbleSetJSON(opv2OpKey(id), &OperationV2Row{}))
	require.NoError(t, p.db.Set(opv2ActKey(id), nil, pebble.Sync))
	require.NoError(t, p.pebbleSetJSON(opv2StateKey(id), &OpStateV2Row{OperationID: id}))
}

func TestSweepHollowOperationsV2RemovesOnlyShells(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	p := store.(*PebbleStore)

	now := time.Now().UTC()
	real := OperationV2Row{ID: "01REAL000000000000000000000", DefID: "library.scan", Plugin: "library", Status: "running", QueuedAt: now, StartedAt: &now}
	require.NoError(t, p.InsertOperationV2(real))
	seedHollowOpV2(t, p, "01HOLLOW00000000000000000000")
	// A value that is not even JSON is hollow too: nothing can read it back.
	require.NoError(t, p.db.Set(opv2OpKey("01GARBAGE0000000000000000000"), []byte("not json"), pebble.Sync))

	rows, err := p.ListOperationsV2Since(now.Add(-time.Hour), 100)
	require.NoError(t, err)
	require.Len(t, rows, 2, "before the sweep the timeline carries the shell")

	n, err := p.SweepHollowOperationsV2()
	require.NoError(t, err)
	require.Equal(t, 2, n)

	rows, err = p.ListOperationsV2Since(now.Add(-time.Hour), 100)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, real.ID, rows[0].ID)

	for _, key := range [][]byte{opv2OpKey("01HOLLOW00000000000000000000"), opv2ActKey("01HOLLOW00000000000000000000"), opv2StateKey("01HOLLOW00000000000000000000")} {
		_, closer, gerr := p.db.Get(key)
		if gerr == nil {
			closer.Close()
		}
		require.ErrorIs(t, gerr, pebble.ErrNotFound, "%s survived the sweep", key)
	}
	got, err := p.GetOperationV2(real.ID)
	require.NoError(t, err)
	require.NotNil(t, got)

	n, err = p.SweepHollowOperationsV2()
	require.NoError(t, err)
	require.Equal(t, 0, n, "a second sweep must find nothing")
}

func TestMigration062SweepsHollowRowsAndIsIdempotent(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	p := store.(*PebbleStore)
	seedHollowOpV2(t, p, "01HOLLOW00000000000000000000")

	require.NoError(t, migration062Up(p))
	rows, err := p.ListOperationsV2Since(time.Now().Add(-time.Hour), 100)
	require.NoError(t, err)
	require.Empty(t, rows)
	require.NoError(t, migration062Up(p), "second run on a clean store")
}
