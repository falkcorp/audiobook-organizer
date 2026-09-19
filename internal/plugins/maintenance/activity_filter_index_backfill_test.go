// file: internal/plugins/maintenance/activity_filter_index_backfill_test.go
// version: 1.0.0
// guid: dc247b67-8e09-445e-82bd-0249e292a052
// last-edited: 2026-09-19

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type afibDeps struct {
	fakeDeps
	store database.ActivityFilterIndexBackfiller
}

func (d *afibDeps) ActivityFilterIndexBackfiller() database.ActivityFilterIndexBackfiller {
	return d.store
}

// afibCancelAfter delegates to the real store and cancels the run's context
// once `after` windows have completed — a deploy restart in the middle of the
// backfill.
type afibCancelAfter struct {
	database.ActivityFilterIndexBackfiller
	after  int32
	calls  atomic.Int32
	cancel context.CancelFunc
}

func (c *afibCancelAfter) BackfillFilterIndexWindow(ctx context.Context, from, to int64, open bool) (int, error) {
	n, err := c.ActivityFilterIndexBackfiller.BackfillFilterIndexWindow(ctx, from, to, open)
	if c.calls.Add(1) == c.after && c.cancel != nil {
		c.cancel()
	}
	return n, err
}

// afibSpy records checkpoints and the result. It is safe for RunItems' worker
// goroutines, which call UpdateProgress concurrently (resultReporter is not).
type afibSpy struct {
	fakeReporter
	mu     sync.Mutex
	states []activityFilterIndexBackfillParams
	result any
}

func (r *afibSpy) SetResult(v any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.result = v
	return nil
}

func (r *afibSpy) UpdateProgress(_, _ int, _ string) error { return nil }

func (r *afibSpy) Checkpoint(state any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := state.(activityFilterIndexBackfillParams); ok {
		r.states = append(r.states, p)
	}
	return nil
}

// newAfibStore seeds rows over `days` days with the filter indexes removed,
// i.e. the state a store written by an older build is in.
func newAfibStore(t *testing.T, days int) (*database.PebbleActivityStore, time.Time) {
	t.Helper()
	db, err := pebble.Open(filepath.Join(t.TempDir(), "a.pebble"), &pebble.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	s := database.NewPebbleActivityStore(db)
	start := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour)
	var batch []database.ActivityEntry
	for i := 0; i < days*4; i++ {
		batch = append(batch, database.ActivityEntry{
			Timestamp: start.Add(time.Duration(i) * 6 * time.Hour),
			Tier:      "change", Type: "t", Level: "info",
			Source: fmt.Sprintf("src%d", i%3), Summary: "row",
		})
	}
	_, err = s.RecordBatch(batch)
	require.NoError(t, err)
	for _, p := range []string{"act:src:", "act:typ:", "act:lvl:"} {
		require.NoError(t, db.DeleteRange([]byte(p), []byte(p[:len(p)-1]+";"), pebble.Sync))
	}
	return s, start
}

func countSource(t *testing.T, s *database.PebbleActivityStore, src string) (int, bool) {
	t.Helper()
	res, err := s.QueryWithPartial(context.Background(), database.ActivityFilter{Source: src, Limit: 1000})
	require.NoError(t, err)
	return res.Total, res.Partial
}

func TestActivityFilterIndexBackfill_ResumesAfterCancelAndGatesPlanner(t *testing.T) {
	s, _ := newAfibStore(t, 40)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wrapped := &afibCancelAfter{ActivityFilterIndexBackfiller: s, after: 10, cancel: cancel}
	p := New(&afibDeps{store: wrapped})
	spy := &afibSpy{}

	err := p.runActivityFilterIndexBackfill(ctx, nil, spy)
	require.Error(t, err, "the cancelled run must fail, not report success")
	assert.False(t, s.FilterIndexBackfillDone(), "an interrupted backfill must not open the gate")

	spy.mu.Lock()
	require.NotEmpty(t, spy.states, "ResumeRestart op must checkpoint")
	last := spy.states[len(spy.states)-1]
	spy.mu.Unlock()
	require.Equal(t, 41, last.PlanWindows, "40 days + the open-ended window")
	require.Positive(t, last.ResumeFrom)
	require.Less(t, last.ResumeFrom, last.PlanWindows)

	// Resume with the checkpoint merged into params, as the registry does.
	raw, err := json.Marshal(last)
	require.NoError(t, err)
	resumed := &afibCancelAfter{ActivityFilterIndexBackfiller: s}
	p2 := New(&afibDeps{store: resumed})
	spy2 := &afibSpy{}
	require.NoError(t, p2.runActivityFilterIndexBackfill(context.Background(), raw, spy2))
	assert.Equal(t, int32(last.PlanWindows-last.ResumeFrom), resumed.calls.Load(),
		"the resumed run starts at the watermark and runs each remaining window once")
	assert.True(t, s.FilterIndexBackfillDone())

	res, ok := spy2.result.(ActivityFilterIndexBackfillResult)
	require.True(t, ok)
	assert.True(t, res.SentinelMarked)

	// Every row is reachable through the index now: 160 rows over 3 sources.
	total := 0
	for i := 0; i < 3; i++ {
		n, partial := countSource(t, s, fmt.Sprintf("src%d", i))
		assert.False(t, partial)
		total += n
	}
	assert.Equal(t, 160, total)
}

func TestActivityFilterIndexBackfill_AlreadyDoneIsANoOp(t *testing.T) {
	s, _ := newAfibStore(t, 2)
	require.NoError(t, s.MarkFilterIndexBackfillDone())
	counting := &afibCancelAfter{ActivityFilterIndexBackfiller: s}
	spy := &afibSpy{}
	require.NoError(t, New(&afibDeps{store: counting}).runActivityFilterIndexBackfill(context.Background(), nil, spy))
	assert.Equal(t, int32(0), counting.calls.Load())
	res, _ := spy.result.(ActivityFilterIndexBackfillResult)
	assert.True(t, res.AlreadyDone)
}

func TestActivityFilterIndexBackfill_RefusesWithoutPebbleStore(t *testing.T) {
	err := New(&afibDeps{}).runActivityFilterIndexBackfill(context.Background(), nil, &afibSpy{})
	require.Error(t, err)
}

func TestActivityFilterIndexBackfill_DefSharesCompactionKey(t *testing.T) {
	p := New(&afibDeps{})
	def := p.activityFilterIndexBackfillDef()
	assert.Equal(t, p.nightlyCompactActivityLogDef().ConcurrencyKey, def.ConcurrencyKey,
		"must never run while compaction deletes rows")
	assert.Equal(t, "maintenance.activity-filter-index-backfill", def.ID)
}

func TestActivityFilterIndexWindowBounds_CoverEverything(t *testing.T) {
	p := activityFilterIndexBackfillParams{PlanStartNanos: 1_000, PlanWindows: 3}
	day := int64(activityFilterIndexWindow)
	f0, t0, o0 := activityFilterIndexWindowBounds(p, 0)
	f1, t1, o1 := activityFilterIndexWindowBounds(p, 1)
	f2, _, o2 := activityFilterIndexWindowBounds(p, 2)
	assert.Equal(t, int64(0), f0, "window 0 starts at the epoch")
	assert.Equal(t, t0, f1)
	assert.Equal(t, t1, f2)
	assert.Equal(t, 1_000+day, t0)
	assert.False(t, o0)
	assert.False(t, o1)
	assert.True(t, o2, "the last window is open-ended")
}
