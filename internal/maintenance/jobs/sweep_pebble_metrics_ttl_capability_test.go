// file: internal/maintenance/jobs/sweep_pebble_metrics_ttl_capability_test.go
// version: 0.1.0
// guid: 9b4d7a02-e6c1-4f38-a5d9-3c8e1f7b0264
// last-edited: 2026-11-20

package jobs

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// sweeperStore is a store that offers the sweep itself, as the books module's
// job store does.
type sweeperStore struct {
	database.Store
	calls   int
	deleted int64
}

func (s *sweeperStore) SweepExpiredMetrics() (int64, error) {
	s.calls++
	return s.deleted, nil
}

type sweepReporter struct {
	logs  []string
	total int
}

func (r *sweepReporter) SetTotal(n int)                      { r.total = n }
func (r *sweepReporter) Increment()                          {}
func (r *sweepReporter) Log(_ string, msg string, _ *string) { r.logs = append(r.logs, msg) }

// TestResolveMetricsSweeper_PrefersTheStoresOwnSweep: a store that carries the
// sweep is used as it stands. The books module's job store has no Unwrap, so
// before this capability the job resolved no Pebble store through it and
// skipped on every run.
func TestResolveMetricsSweeper_PrefersTheStoresOwnSweep(t *testing.T) {
	s := &sweeperStore{deleted: 7}
	got := resolveMetricsSweeper(s)
	require.NotNil(t, got)
	n, err := got.SweepExpiredMetrics()
	require.NoError(t, err)
	require.Equal(t, int64(7), n)
	require.Equal(t, 1, s.calls)
}

// TestResolveMetricsSweeper_NonPebbleBackendIsNil pins the explicit nil check:
// a nil *PebbleMetricsStore converted to the interface would be a non-nil
// sweeper that panics on first use.
func TestResolveMetricsSweeper_NonPebbleBackendIsNil(t *testing.T) {
	type plain struct{ database.Store }
	got := resolveMetricsSweeper(plain{})
	require.True(t, got == nil, "a store that is neither a sweeper nor a Pebble backend resolves to no sweeper, got %T", got)
}

// TestResolveMetricsSweeper_PebbleStoreFallsBackToTheMetricsStore is the bare
// path every non-books-module run takes: the store has no sweep of its own, and
// the metrics store is built from the Pebble store behind it.
func TestResolveMetricsSweeper_PebbleStoreFallsBackToTheMetricsStore(t *testing.T) {
	ps, err := database.NewPebbleStoreInMemory(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })

	got := resolveMetricsSweeper(ps)
	require.NotNil(t, got)
	_, isMetrics := got.(*database.PebbleMetricsStore)
	require.True(t, isMetrics, "resolved %T, want the metrics store built from the Pebble store", got)
}

func TestSweepPebbleMetricsTTL_RunThroughTheStoresOwnSweep(t *testing.T) {
	j := &sweepPebbleMetricsTTLJob{}

	dry := &sweeperStore{deleted: 3}
	rep := &sweepReporter{}
	require.NoError(t, j.Run(context.Background(), dry, rep, true))
	require.Zero(t, dry.calls, "a dry run sweeps nothing")

	live := &sweeperStore{deleted: 3}
	rep = &sweepReporter{}
	require.NoError(t, j.Run(context.Background(), live, rep, false))
	require.Equal(t, 1, live.calls)
	require.Equal(t, 3, rep.total)
	require.Contains(t, rep.logs, "Swept 3 expired metrics snapshot(s)")
}

func TestSweepPebbleMetricsTTL_RunSkipsANonPebbleBackend(t *testing.T) {
	type plain struct{ database.Store }
	rep := &sweepReporter{}
	require.NoError(t, (&sweepPebbleMetricsTTLJob{}).Run(context.Background(), plain{}, rep, false))
	require.Equal(t, []string{"Store is not PebbleStore — skipped"}, rep.logs)
}
