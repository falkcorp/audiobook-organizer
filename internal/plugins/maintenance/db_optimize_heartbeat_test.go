// file: internal/plugins/maintenance/db_optimize_heartbeat_test.go
// version: 1.0.0
// guid: 6b27f0a4-91c3-4de8-8a05-3c7e1b94d260
// last-edited: 2026-09-08

// maintenance.db-optimize could not finish on a real database.
//
// store.Optimize() is one blocking `db.Compact(nil, 0xff)` with no callback,
// and runDBOptimize reported progress only at its three step boundaries. The
// operations registry cancels an op that reports nothing for five minutes, so
// on production -- 31 GB, a full compaction well past 5m -- it was killed every
// single time, weekly, on its 0 2 * * 0 schedule. Measured 2026-09-08: enqueued
// 10:25:45, "canceling stuck op ... no progress for 5m9s" at 10:30:54.
//
// The tempting fix is a bare ticker. That would satisfy the watchdog and
// destroy it in the same stroke: a genuinely wedged compaction would report
// healthy forever. registry/types.go:200 already records that raising
// ProgressTimeout "hides the first case permanently", and an unconditional
// heartbeat is that same mistake in a different costume.
//
// So the contract under test is narrow and two-sided:
//
//	counters moved  -> report, and the op survives
//	counters flat   -> say NOTHING, and let the watchdog kill it
//
// The second case is the one that matters. A test that only checked "does it
// report" would pass just as happily against the broken blind-ticker version.
package maintenance

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// heartbeatReporter records what UpdateProgress was told, which the package's
// existing fakeReporter discards.
type heartbeatReporter struct {
	mu       sync.Mutex
	progress []string
}

func (r *heartbeatReporter) UpdateProgress(_, _ int, msg string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.progress = append(r.progress, msg)
	return nil
}
func (r *heartbeatReporter) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.progress)
}
func (r *heartbeatReporter) Log(_ slog.Level, _ string, _ ...slog.Attr) error { return nil }
func (r *heartbeatReporter) Logger() *slog.Logger                             { return slog.Default() }
func (r *heartbeatReporter) Checkpoint(_ any) error                           { return nil }
func (r *heartbeatReporter) IsCanceled() bool                                 { return false }
func (r *heartbeatReporter) RunPhase(_ context.Context, _ string, fn func(context.Context, registry.Reporter) error) error {
	return fn(context.Background(), r)
}
func (r *heartbeatReporter) Trigger(_ context.Context, _ string, _ any) error { return nil }
func (r *heartbeatReporter) SetCurrentItem(_ string)                          {}

var _ sdk.Reporter = (*heartbeatReporter)(nil)

// compactStub stands in for the store. Optimize blocks until release is closed,
// exactly like a long compaction, so the heartbeat has something to tick over.
type compactStub struct {
	mu      sync.Mutex
	stats   database.CompactionStats
	release chan struct{}
	err     error
	// onStats mutates stats each time they are read, standing in for an engine
	// making (or failing to make) progress.
	onStats func(*database.CompactionStats)
}

func (s *compactStub) Optimize(_ context.Context) error {
	<-s.release
	return s.err
}

func (s *compactStub) CompactionStats() database.CompactionStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.onStats != nil {
		s.onStats(&s.stats)
	}
	return s.stats
}

var _ compactableStore = (*compactStub)(nil)

const testTick = 5 * time.Millisecond

func TestOptimizeMainWithHeartbeat_ReportsWhenCompactionIsMakingProgress(t *testing.T) {
	rep := &heartbeatReporter{}
	stub := &compactStub{
		release: make(chan struct{}),
		stats:   database.CompactionStats{EstimatedDebt: 1000, NumInProgress: 1},
		// Debt falls on every read: the engine is working.
		onStats: func(c *database.CompactionStats) {
			if c.EstimatedDebt >= 10 {
				c.EstimatedDebt -= 10
			}
		},
	}

	done := make(chan error, 1)
	go func() { done <- optimizeMainWithHeartbeat(context.Background(), stub, rep, 3, testTick) }()

	// Give it several ticks, then let the compaction finish.
	require.Eventually(t, func() bool { return rep.count() >= 3 }, 2*time.Second, testTick,
		"a compaction whose debt is falling must report progress, or the watchdog kills it at 5m")
	close(stub.release)
	require.NoError(t, <-done)

	// The message has to carry something an operator can read a trend from,
	// not just the word "working".
	rep.mu.Lock()
	first := rep.progress[0]
	rep.mu.Unlock()
	assert.Contains(t, first, "left to compact")
	assert.Contains(t, first, "elapsed")
}

func TestOptimizeMainWithHeartbeat_SaysNothingWhenCountersAreFlat(t *testing.T) {
	// THE test. A wedged compaction -- in progress, but neither the debt nor
	// the completed count moving -- must produce NO progress reports, so the
	// registry's stuck detector still fires. A blind ticker passes the test
	// above and fails this one.
	rep := &heartbeatReporter{}
	stub := &compactStub{
		release: make(chan struct{}),
		stats:   database.CompactionStats{EstimatedDebt: 1000, Count: 7, NumInProgress: 1},
		onStats: nil, // nothing changes, ever
	}

	done := make(chan error, 1)
	go func() { done <- optimizeMainWithHeartbeat(context.Background(), stub, rep, 3, testTick) }()

	// Long enough for many ticks to have fired.
	time.Sleep(40 * testTick)
	assert.Zero(t, rep.count(),
		"a stalled compaction must NOT report progress -- staying silent is what lets the watchdog catch it")

	close(stub.release)
	require.NoError(t, <-done)
}

func TestOptimizeMainWithHeartbeat_ReportsOnCompletedCompactionsAlone(t *testing.T) {
	// EstimatedDebt is an estimate and can sit still or even rise while real
	// work completes. A rising completed-compaction count is progress on its
	// own, so the predicate is an OR, not an AND.
	rep := &heartbeatReporter{}
	stub := &compactStub{
		release: make(chan struct{}),
		stats:   database.CompactionStats{EstimatedDebt: 1000, Count: 0, NumInProgress: 1},
		onStats: func(c *database.CompactionStats) { c.Count++ }, // debt never moves
	}

	done := make(chan error, 1)
	go func() { done <- optimizeMainWithHeartbeat(context.Background(), stub, rep, 3, testTick) }()

	require.Eventually(t, func() bool { return rep.count() >= 2 }, 2*time.Second, testTick,
		"completed compactions are progress even when the debt estimate is flat")
	close(stub.release)
	require.NoError(t, <-done)
}

func TestOptimizeMainWithHeartbeat_PropagatesOptimizeError(t *testing.T) {
	// The heartbeat must not swallow the outcome it was wrapped around.
	sentinel := errors.New("compaction exploded")
	rep := &heartbeatReporter{}
	stub := &compactStub{release: make(chan struct{}), err: sentinel}
	close(stub.release)

	err := optimizeMainWithHeartbeat(context.Background(), stub, rep, 3, testTick)
	require.ErrorIs(t, err, sentinel)
}

func TestOptimizeMainWithHeartbeat_ReturnsImmediatelyWhenCompactionIsFast(t *testing.T) {
	// A small database compacts in well under one tick. That must return at
	// once rather than waiting out the interval.
	rep := &heartbeatReporter{}
	stub := &compactStub{release: make(chan struct{})}
	close(stub.release)

	start := time.Now()
	require.NoError(t, optimizeMainWithHeartbeat(context.Background(), stub, rep, 3, time.Hour))
	assert.Less(t, time.Since(start), 5*time.Second,
		"completion must be selected on directly, not gated behind the tick")
	assert.Zero(t, rep.count(), "a compaction that never ticked reports nothing")
}
