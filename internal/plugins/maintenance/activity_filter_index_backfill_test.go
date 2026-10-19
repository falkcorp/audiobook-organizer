// file: internal/plugins/maintenance/activity_filter_index_backfill_test.go
// version: 1.1.5
// guid: dc247b67-8e09-445e-82bd-0249e292a052
// last-edited: 2026-10-19

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
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
	s := database.NewPebbleActivityStore(database.NewGatedDB(db))
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

	// One worker, not NumCPU: on a 48-core host all 41 windows ran at once,
	// finished in any order, and the contiguous watermark often never reached
	// the first checkpoint (every 8) before the cancel after 10 calls. Any
	// pool > 1 keeps that race; out-of-order watermarks are covered by the
	// registry's own RunItems tests.
	err := p.runActivityFilterIndexBackfill(ctx, json.RawMessage(`{"workers":1}`), spy)
	require.Error(t, err, "the cancelled run must fail, not report success")
	assert.False(t, s.FilterIndexBackfillDone(), "an interrupted backfill must not open the gate")

	spy.mu.Lock()
	require.NotEmpty(t, spy.states, "ResumeRestart op must checkpoint")
	last := spy.states[len(spy.states)-1]
	spy.mu.Unlock()
	require.Equal(t, 41, last.PlanWindows, "40 days + the open-ended window")
	require.Positive(t, last.ResumeFrom)
	require.Less(t, last.ResumeFrom, last.PlanWindows)

	// Resume with the checkpoint merged into params, as the registry does, but
	// with the pool back at its NumCPU default: only the cancelled leg needs
	// the pin, and this leg's call count and row total don't depend on order.
	last.Workers = 0
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

// afibGateSpy records whether the planner gate was open when each window ran.
type afibGateSpy struct {
	database.ActivityFilterIndexBackfiller
	mu       sync.Mutex
	gateSeen []bool
}

func (g *afibGateSpy) BackfillFilterIndexWindow(ctx context.Context, from, to int64, open bool) (int, error) {
	g.mu.Lock()
	g.gateSeen = append(g.gateSeen, g.ActivityFilterIndexBackfiller.FilterIndexBackfillDone())
	g.mu.Unlock()
	return g.ActivityFilterIndexBackfiller.BackfillFilterIndexWindow(ctx, from, to, open)
}

// TestActivityFilterIndexBackfill_ForceClosesGateBeforeFirstWindow: a forced
// rebuild must stop the planner trusting the index BEFORE it starts writing,
// or queries during the rebuild return short pages with partial=false.
func TestActivityFilterIndexBackfill_ForceClosesGateBeforeFirstWindow(t *testing.T) {
	s, _ := newAfibStore(t, 5)
	require.NoError(t, s.MarkFilterIndexBackfillDone())
	spy := &afibGateSpy{ActivityFilterIndexBackfiller: s}
	require.NoError(t, New(&afibDeps{store: spy}).runActivityFilterIndexBackfill(
		context.Background(), json.RawMessage(`{"force":true}`), &afibSpy{}))
	spy.mu.Lock()
	defer spy.mu.Unlock()
	require.NotEmpty(t, spy.gateSeen)
	for i, open := range spy.gateSeen {
		assert.False(t, open, "window %d ran with the planner gate open", i)
	}
	assert.True(t, s.FilterIndexBackfillDone(), "re-opened when the forced run completes")
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

// --- reconcile mode (04-P6 part 2) ---

// reconcileSpy is a reporter that keeps each line's level and no-op tag, counts
// the liveness stamps and records the persisted result.
type reconcileSpy struct {
	p6AttrReporter
	smu    sync.Mutex
	stamps int
	result any
}

func (r *reconcileSpy) UpdateProgress(_, _ int, _ string) error {
	r.smu.Lock()
	r.stamps++
	r.smu.Unlock()
	return nil
}

func (r *reconcileSpy) SetResult(v any) error {
	r.smu.Lock()
	r.result = v
	r.smu.Unlock()
	return nil
}

func (r *reconcileSpy) text() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var b strings.Builder
	for _, l := range r.lines {
		b.WriteString(l.msg + "\n")
	}
	return b.String()
}

// fakeReconcileStore is the activity store as the reconcile sees it: the
// sentinel, and ReconcileFilterIndexCoverage's answer. The embedded interface
// is nil; the reconcile mode never calls the backfill methods.
type fakeReconcileStore struct {
	database.ActivityFilterIndexBackfiller
	mu        sync.Mutex
	done      bool
	reconcile func(ctx context.Context, progress database.FilterIndexProgress) (database.FilterIndexReconcileResult, error)
	calls     int
}

func (f *fakeReconcileStore) FilterIndexBackfillDone() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.done
}

func (f *fakeReconcileStore) withdraw() {
	f.mu.Lock()
	f.done = false
	f.mu.Unlock()
}

func (f *fakeReconcileStore) ReconcileFilterIndexCoverage(ctx context.Context, progress database.FilterIndexProgress) (database.FilterIndexReconcileResult, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.reconcile == nil {
		return database.FilterIndexReconcileResult{}, nil
	}
	return f.reconcile(ctx, progress)
}

func reconcilePlugin(store database.ActivityFilterIndexBackfiller) *Plugin {
	return New(&afibDeps{store: store})
}

var reconcileParams = json.RawMessage(`{"reconcile":true}`)

// The reconcile is not the full backfill with a different name: with the
// sentinel set (the usual boot) the full backfill's early return says
// "already complete" and never looks for a gap. The reconcile branch comes
// first, so the store is asked.
func TestActivityFilterIndexReconcile_RunsBeforeTheAlreadyCompleteReturn(t *testing.T) {
	f := &fakeReconcileStore{done: true}
	rep := &reconcileSpy{}
	if err := reconcilePlugin(f).runActivityFilterIndexBackfill(context.Background(), reconcileParams, rep); err != nil {
		t.Fatal(err)
	}
	if f.calls != 1 {
		t.Fatalf("ReconcileFilterIndexCoverage calls = %d, want 1 (a sentinel-set store must still be reconciled)", f.calls)
	}
	if strings.Contains(rep.text(), "already complete") {
		t.Errorf("the reconcile took the full backfill's already-complete path: %s", rep.text())
	}
}

func TestActivityFilterIndexReconcile_RefusesForce(t *testing.T) {
	f := &fakeReconcileStore{done: true}
	err := reconcilePlugin(f).runActivityFilterIndexBackfill(context.Background(),
		json.RawMessage(`{"reconcile":true,"force":true}`), &reconcileSpy{})
	if err == nil || !strings.Contains(err.Error(), "cannot be combined with force") {
		t.Fatalf("err = %v, want reconcile+force refused", err)
	}
	if f.calls != 0 {
		t.Fatalf("a refused run reached the store (%d calls)", f.calls)
	}
}

// Absent reconcile keeps the full backfill exactly as before: a completed
// sentinel is "already complete", and the reconcile is never called.
func TestActivityFilterIndexReconcile_AbsentParamIsTheFullBackfill(t *testing.T) {
	s, _ := newAfibStore(t, 3)
	require.NoError(t, s.MarkFilterIndexBackfillDone())
	spy := &fakeReconcileStore{ActivityFilterIndexBackfiller: s, done: true}
	rep := &reconcileSpy{}
	require.NoError(t, reconcilePlugin(spy).runActivityFilterIndexBackfill(context.Background(), json.RawMessage(`{}`), rep))
	assert.Equal(t, 0, spy.calls, "the full backfill must not reconcile")
	assert.Contains(t, rep.text(), "already complete")
}

func TestActivityFilterIndexReconcile_NoGapIsATaggedNoOp(t *testing.T) {
	f := &fakeReconcileStore{done: true, reconcile: func(_ context.Context, _ database.FilterIndexProgress) (database.FilterIndexReconcileResult, error) {
		return database.FilterIndexReconcileResult{SeenThrough: 42}, nil
	}}
	rep := &reconcileSpy{}
	require.NoError(t, reconcilePlugin(f).runActivityFilterIndexBackfill(context.Background(), reconcileParams, rep))
	assert.Equal(t, 1, rep.noOpLines())
	assert.False(t, hasLevel(&rep.p6AttrReporter, slog.LevelError))
	assert.False(t, hasLevel(&rep.p6AttrReporter, slog.LevelWarn))
	assert.Nil(t, rep.result)
}

func TestActivityFilterIndexReconcile_GapFoundWarnsAndPersistsResult(t *testing.T) {
	f := &fakeReconcileStore{done: true, reconcile: func(_ context.Context, progress database.FilterIndexProgress) (database.FilterIndexReconcileResult, error) {
		progress("reindex", 30)
		return database.FilterIndexReconcileResult{SeenThrough: 42, GapFound: true, Reindexed: 30}, nil
	}}
	rep := &reconcileSpy{}
	require.NoError(t, reconcilePlugin(f).runActivityFilterIndexBackfill(context.Background(), reconcileParams, rep))
	assert.Equal(t, 0, rep.noOpLines(), "a run that indexed rows is not a no-op")
	assert.True(t, hasLevel(&rep.p6AttrReporter, slog.LevelWarn))
	res, ok := rep.result.(ActivityFilterIndexBackfillResult)
	require.True(t, ok, "result = %#v", rep.result)
	assert.Equal(t, int64(30), res.RowsIndexed)
	assert.True(t, res.SentinelMarked)
}

// Sentinel not set: nothing to reconcile (the full backfill covers every row),
// but the line names the op so the standing state is findable.
func TestActivityFilterIndexReconcile_SentinelNotSetIsATaggedNoOpNamingTheOp(t *testing.T) {
	f := &fakeReconcileStore{done: false}
	rep := &reconcileSpy{}
	require.NoError(t, reconcilePlugin(f).runActivityFilterIndexBackfill(context.Background(), reconcileParams, rep))
	assert.Equal(t, 0, f.calls)
	assert.Equal(t, 1, rep.noOpLines())
	assert.Contains(t, rep.text(), "maintenance.activity-filter-index-backfill")
}

// A non-Pebble activity backend has no filter index: boot must not be red every
// start. The full backfill still refuses.
func TestActivityFilterIndexReconcile_NoStoreIsATaggedNoOp(t *testing.T) {
	p := reconcilePlugin(nil)
	rep := &reconcileSpy{}
	require.NoError(t, p.runActivityFilterIndexBackfill(context.Background(), reconcileParams, rep))
	assert.True(t, hasLevel(&rep.p6AttrReporter, slog.LevelWarn))
	assert.Equal(t, 1, rep.noOpLines())
	err := p.runActivityFilterIndexBackfill(context.Background(), json.RawMessage(`{}`), &reconcileSpy{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no Pebble activity store is wired")
}

// A store that cannot reconcile is a wiring bug, not a quiet success.
func TestActivityFilterIndexReconcile_StoreWithoutReconcileFails(t *testing.T) {
	type bare struct {
		database.ActivityFilterIndexBackfiller
	}
	err := reconcilePlugin(bare{}).runActivityFilterIndexBackfill(context.Background(), reconcileParams, &reconcileSpy{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ReconcileFilterIndexCoverage")
}

// A failure, or a cancel, after the store withdrew the sentinel leaves indexed
// filtering off, and nothing re-enables it but the full backfill: both the Error
// line and the returned error say which op to run.
func TestActivityFilterIndexReconcile_FailureAndCancelNameTheOp(t *testing.T) {
	const opName = "maintenance.activity-filter-index-backfill"
	t.Run("failure", func(t *testing.T) {
		boom := errors.New("boom")
		f := &fakeReconcileStore{done: true, reconcile: func(context.Context, database.FilterIndexProgress) (database.FilterIndexReconcileResult, error) {
			return database.FilterIndexReconcileResult{SeenThrough: 7, GapFound: true, Reindexed: 3}, boom
		}}
		rep := &reconcileSpy{}
		err := reconcilePlugin(f).runActivityFilterIndexBackfill(context.Background(), reconcileParams, rep)
		require.ErrorIs(t, err, boom)
		assert.Contains(t, err.Error(), opName)
		assert.Contains(t, err.Error(), "seen-through=7")
		assert.True(t, hasLevel(&rep.p6AttrReporter, slog.LevelError))
		assert.Contains(t, rep.text(), opName)
	})
	t.Run("cancel after the sentinel was withdrawn", func(t *testing.T) {
		started := make(chan struct{})
		f := &fakeReconcileStore{done: true}
		f.reconcile = func(ctx context.Context, _ database.FilterIndexProgress) (database.FilterIndexReconcileResult, error) {
			f.withdraw()
			close(started)
			<-ctx.Done()
			return database.FilterIndexReconcileResult{SeenThrough: 7, GapFound: true, Reindexed: 3},
				fmt.Errorf("pebble_activity_store: reindex rollback gap: %w", ctx.Err())
		}
		ctx, cancel := context.WithCancel(context.Background())
		rep := &reconcileSpy{}
		done := make(chan error, 1)
		go func() { done <- reconcilePlugin(f).runActivityFilterIndexBackfill(ctx, reconcileParams, rep) }()
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			cancel()
			t.Fatal("the reconcile never started")
		}
		cancel()
		select {
		case err := <-done:
			require.ErrorIs(t, err, context.Canceled)
			assert.Contains(t, err.Error(), opName, "the cancel path must name the op to run")
		case <-time.After(2 * time.Second):
			t.Fatal("the reconcile did not return within 2s of its ctx being cancelled")
		}
		assert.True(t, hasLevel(&rep.p6AttrReporter, slog.LevelError), "a withdrawn sentinel is an Error line")
		assert.Contains(t, rep.text(), opName)
	})
	t.Run("cancel before anything was withdrawn", func(t *testing.T) {
		started := make(chan struct{})
		f := &fakeReconcileStore{done: true, reconcile: func(ctx context.Context, _ database.FilterIndexProgress) (database.FilterIndexReconcileResult, error) {
			close(started)
			<-ctx.Done()
			return database.FilterIndexReconcileResult{SeenThrough: 7}, ctx.Err()
		}}
		ctx, cancel := context.WithCancel(context.Background())
		rep := &reconcileSpy{}
		done := make(chan error, 1)
		go func() { done <- reconcilePlugin(f).runActivityFilterIndexBackfill(ctx, reconcileParams, rep) }()
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			cancel()
			t.Fatal("the reconcile never started")
		}
		cancel()
		select {
		case err := <-done:
			require.ErrorIs(t, err, context.Canceled)
		case <-time.After(2 * time.Second):
			t.Fatal("the reconcile did not return within 2s of its ctx being cancelled")
		}
		assert.False(t, hasLevel(&rep.p6AttrReporter, slog.LevelError), "an interruption that changed nothing is not an Error")
		assert.True(t, hasLevel(&rep.p6AttrReporter, slog.LevelInfo))
	})
	t.Run("timeout is a failure", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		f := &fakeReconcileStore{done: true, reconcile: func(ctx context.Context, _ database.FilterIndexProgress) (database.FilterIndexReconcileResult, error) {
			return database.FilterIndexReconcileResult{GapFound: true}, fmt.Errorf("reindex: %w", ctx.Err())
		}}
		rep := &reconcileSpy{}
		err := reconcilePlugin(f).runActivityFilterIndexBackfill(ctx, reconcileParams, rep)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		assert.NotContains(t, rep.text(), "interrupted")
		assert.True(t, hasLevel(&rep.p6AttrReporter, slog.LevelError))
	})
}

// The reconcile's progress callback stamps the watchdog's liveness clock, so a
// long reindex is never silent: the start stamp plus one per callback.
func TestActivityFilterIndexReconcile_ProgressStampsLiveness(t *testing.T) {
	f := &fakeReconcileStore{done: true, reconcile: func(_ context.Context, progress database.FilterIndexProgress) (database.FilterIndexReconcileResult, error) {
		progress("check", 64)
		progress("reindex", 2000)
		progress("reindex", 4000)
		return database.FilterIndexReconcileResult{}, nil
	}}
	rep := &reconcileSpy{}
	require.NoError(t, reconcilePlugin(f).runActivityFilterIndexBackfill(context.Background(), reconcileParams, rep))
	if rep.stamps < 4 {
		t.Fatalf("UpdateProgress calls = %d, want at least 4 (start and three callbacks)", rep.stamps)
	}
}

// The gap check reports every few dozen rows; the op stamps liveness at most
// once an interval for it (a reindex chunk always stamps), so a long check
// does not rewrite the op row thousands of times.
func TestActivityFilterIndexReconcile_CheckStampsAreThrottled(t *testing.T) {
	prevEvery, prevNow := activityFilterIndexCheckStampEvery, activityFilterIndexNow
	t.Cleanup(func() { activityFilterIndexCheckStampEvery, activityFilterIndexNow = prevEvery, prevNow })
	now := time.Unix(1_700_000_000, 0)
	activityFilterIndexCheckStampEvery = time.Second
	activityFilterIndexNow = func() time.Time { return now }

	f := &fakeReconcileStore{done: true, reconcile: func(_ context.Context, progress database.FilterIndexProgress) (database.FilterIndexReconcileResult, error) {
		for i := 1; i <= 10; i++ { // ten reports inside one interval
			progress("check", i*64)
		}
		now = now.Add(2 * time.Second)
		progress("check", 704) // the next interval
		progress("reindex", 2000)
		progress("reindex", 4000) // reindex is never throttled
		return database.FilterIndexReconcileResult{}, nil
	}}
	rep := &reconcileSpy{}
	require.NoError(t, reconcilePlugin(f).runActivityFilterIndexBackfill(context.Background(), reconcileParams, rep))
	// start stamp + first check + second-interval check + two reindex chunks
	assert.Equal(t, 5, rep.stamps)
}

// The reconcile stamps liveness by hand, so the def must declare it: the
// RunItems declaration was only true of the full backfill.
func TestActivityFilterIndexBackfillDef_DeclaresManualLiveness(t *testing.T) {
	if got := (&Plugin{}).activityFilterIndexBackfillDef().Liveness; got != sdk.LivenessManual {
		t.Fatalf("Liveness = %v, want LivenessManual", got)
	}
}

// End to end on a real store: a store written by an older build (rows, no
// filter keys, no seen-through mark) under a completed sentinel, booted, and
// this boot has already written a newer row before the op runs. The op still
// finds and indexes the older rows (the check starts from the mark at open),
// after which a filtered query answers in full with partial=false.
func TestActivityFilterIndexReconcile_RealStoreAfterABootWrite(t *testing.T) {
	s, _ := newAfibStore(t, 6)
	require.NoError(t, s.MarkFilterIndexBackfillDone())
	// A rollback wrote these rows after the mark: the mark is gone, the
	// sentinel stays.
	db := s.DB()
	require.NoError(t, db.Delete([]byte(database.ActivityFilterIndexSeenThroughKey), pebble.Sync))
	booted := database.NewPebbleActivityStore(db)
	require.True(t, booted.FilterIndexBackfillDone())
	_, err := booted.Record(database.ActivityEntry{Tier: "change", Type: "t", Level: "info", Source: "src0", Summary: "boot line"})
	require.NoError(t, err)

	rep := &reconcileSpy{}
	require.NoError(t, reconcilePlugin(booted).runActivityFilterIndexBackfill(context.Background(), reconcileParams, rep))
	assert.Equal(t, 0, rep.noOpLines(), "rows were indexed")
	assert.True(t, booted.FilterIndexBackfillDone())
	total, partial := countSource(t, booted, "src0")
	assert.False(t, partial)
	assert.Equal(t, 9, total, "8 older rows of src0 plus the boot line")
}
