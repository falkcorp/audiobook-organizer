// file: internal/database/pebble_activity_closed_test.go
// version: 1.0.0
// guid: 7bcf71c3-3a7a-48f4-93ee-6399233ce17c
// last-edited: 2026-09-14

// Package database — regression suite for PebbleActivityStore on a closed DB.
//
// WHY: PebbleActivityStore does not own its *pebble.DB. It borrows the main
// PebbleStore's (NewPebbleActivityStoreFromStore), and its own Close is a
// no-op, so activity.Service.Close's "never close under an in-flight write"
// guard protects nothing: the DB is closed by the main store's owner. When a
// deferred flush outlived its shutdown budget, the flush goroutine's commit hit
// the closed DB and pebble PANICKED ("pebble: closed"), killing the process
// (CI on #3416 and #3418). A write after close must now come back as an error
// wrapping pebble.ErrClosed, which the callers already log with a count.
package database

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newClosedActivityStore builds an activity store over an in-memory main store
// and then closes the main store underneath it: the production ownership shape.
func newClosedActivityStore(t *testing.T) *PebbleActivityStore {
	t.Helper()
	main, err := NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "main.pebble"))
	require.NoError(t, err)
	act := NewPebbleActivityStoreFromStore(main)
	require.NotNil(t, act)
	require.NoError(t, main.Close())
	return act
}

func TestPebbleActivityStore_WritesAfterBorrowedDBClosedReturnErrClosed(t *testing.T) {
	ts := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)

	t.Run("Record", func(t *testing.T) {
		act := newClosedActivityStore(t)
		var id int64
		var err error
		require.NotPanics(t, func() { id, err = act.Record(batchTestEntry(0, ts)) })
		require.Error(t, err)
		assert.True(t, errors.Is(err, pebble.ErrClosed), "want pebble.ErrClosed, got %v", err)
		assert.Zero(t, id, "a write that never committed must not report an ID")
	})

	t.Run("RecordBatch", func(t *testing.T) {
		act := newClosedActivityStore(t)
		var written int
		var err error
		require.NotPanics(t, func() { written, err = act.RecordBatch(batchTestEntries(3, ts)) })
		require.Error(t, err)
		assert.True(t, errors.Is(err, pebble.ErrClosed), "want pebble.ErrClosed, got %v", err)
		assert.Zero(t, written, "nothing was made durable, so the written count must be 0")
	})
}

// TestPebbleActivityStore_ReadsAndMaintenanceAfterBorrowedDBClosed covers the
// other entry points that can still be running when the main store closes: an
// in-flight activity request, and the nightly maintenance ops.
func TestPebbleActivityStore_ReadsAndMaintenanceAfterBorrowedDBClosed(t *testing.T) {
	ctx := context.Background()
	old := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	cases := map[string]func(*PebbleActivityStore) error{
		"Query": func(s *PebbleActivityStore) error {
			_, _, err := s.Query(ctx, ActivityFilter{Limit: 10})
			return err
		},
		"GetDistinctSources": func(s *PebbleActivityStore) error {
			_, err := s.GetDistinctSources(ctx, ActivityFilter{})
			return err
		},
		"CountActivity": func(s *PebbleActivityStore) error {
			_, err := s.CountActivity(ctx, "change", nil)
			return err
		},
		"Summarize": func(s *PebbleActivityStore) error {
			_, err := s.Summarize(ctx, old, "change")
			return err
		},
		"Prune": func(s *PebbleActivityStore) error {
			_, err := s.Prune(ctx, old, "change")
			return err
		},
		"WipeAllActivity": func(s *PebbleActivityStore) error {
			_, err := s.WipeAllActivity(ctx)
			return err
		},
		"RepairActivityIndexes": func(s *PebbleActivityStore) error {
			_, err := s.RepairActivityIndexes(ctx)
			return err
		},
		"CompactByDay": func(s *PebbleActivityStore) error {
			_, err := s.CompactByDay(ctx, old)
			return err
		},
		"RecompactDigests": func(s *PebbleActivityStore) error {
			_, err := s.RecompactDigests(ctx)
			return err
		},
	}
	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			act := newClosedActivityStore(t)
			var err error
			require.NotPanics(t, func() { err = call(act) })
			require.Error(t, err)
			assert.True(t, errors.Is(err, pebble.ErrClosed), "want pebble.ErrClosed, got %v", err)
		})
	}
}
