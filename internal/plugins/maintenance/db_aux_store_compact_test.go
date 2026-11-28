// file: internal/plugins/maintenance/db_aux_store_compact_test.go
// version: 1.0.1
// guid: 7e3a91c4-2b58-4d06-9f1a-c4d82b6e0a37
// last-edited: 2026-11-18

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"
)

// auxStub stands in for the two auxiliary stores. A store is "owned" when its
// stats sampler answers ok, which is what HEAD's step 2 and 3 branch on.
type auxStub struct {
	mu                 sync.Mutex
	aiOwned, olOwned   bool
	aiErr, olErr       error
	blockFor           time.Duration
	aiCalls, olCalls   int
	aiStatsF, olStatsF func() database.CompactionStats
}

func (a *auxStub) wait(ctx context.Context) error {
	if a.blockFor <= 0 {
		return nil
	}
	select {
	case <-time.After(a.blockFor):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *auxStub) OptimizeAIScanStore(ctx context.Context) error {
	a.mu.Lock()
	a.aiCalls++
	a.mu.Unlock()
	if err := a.wait(ctx); err != nil {
		return err
	}
	return a.aiErr
}

func (a *auxStub) OptimizeOLStore(ctx context.Context) error {
	a.mu.Lock()
	a.olCalls++
	a.mu.Unlock()
	if err := a.wait(ctx); err != nil {
		return err
	}
	return a.olErr
}

func (a *auxStub) AIScanStoreCompactionStats() (database.CompactionStats, bool) {
	if a.aiStatsF != nil {
		return a.aiStatsF(), a.aiOwned
	}
	return database.CompactionStats{}, a.aiOwned
}

func (a *auxStub) OLStoreCompactionStats() (database.CompactionStats, bool) {
	if a.olStatsF != nil {
		return a.olStatsF(), a.olOwned
	}
	return database.CompactionStats{}, a.olOwned
}

func (a *auxStub) calls() (ai, ol int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.aiCalls, a.olCalls
}

var _ StoreOptimizer = (*auxStub)(nil)

// debtFalling reports an engine that works: debt drops on every sample.
func debtFalling() func() database.CompactionStats {
	var mu sync.Mutex
	debt := uint64(1000)
	return func() database.CompactionStats {
		mu.Lock()
		defer mu.Unlock()
		if debt >= 10 {
			debt -= 10
		}
		return database.CompactionStats{EstimatedDebt: debt, NumInProgress: 1}
	}
}

func debtFlat() func() database.CompactionStats {
	return func() database.CompactionStats {
		return database.CompactionStats{EstimatedDebt: 1000, NumInProgress: 1}
	}
}

// auxHost is a FakeRun that records what the kind says: status lines (which
// stamp liveness), bare stamps, the progress index and the run's log.
type auxHost struct {
	*taskstest.FakeRun
	mu      sync.Mutex
	notes   []string
	touches int
	current []int64
	lines   []string
}

func newAuxHost(ctx context.Context) *auxHost {
	return &auxHost{FakeRun: taskstest.NewFakeRun(ctx, auxStoreCompactKind)}
}

func (h *auxHost) Note(m string) { h.mu.Lock(); h.notes = append(h.notes, m); h.mu.Unlock() }
func (h *auxHost) Touch()        { h.mu.Lock(); h.touches++; h.mu.Unlock() }
func (h *auxHost) ReportProgress(done int64, _ tasks.Total) {
	h.mu.Lock()
	h.current = append(h.current, done)
	h.mu.Unlock()
}
func (h *auxHost) Log() *slog.Logger { return slog.New(&auxLines{h: h}) }

func (h *auxHost) snapshot() (notes, lines []string, touches int, current []int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.notes...), append([]string(nil), h.lines...), h.touches, append([]int64(nil), h.current...)
}

type auxLines struct{ h *auxHost }

func (auxLines) Enabled(context.Context, slog.Level) bool { return true }
func (l *auxLines) Handle(_ context.Context, r slog.Record) error {
	l.h.mu.Lock()
	l.h.lines = append(l.h.lines, r.Message)
	l.h.mu.Unlock()
	return nil
}
func (l *auxLines) WithAttrs([]slog.Attr) slog.Handler { return l }
func (l *auxLines) WithGroup(string) slog.Handler      { return l }

// runAux runs the native kind's own Run on a fresh host and decodes its result.
func runAux(t *testing.T, aux StoreOptimizer, interval time.Duration) (*auxHost, auxStoreCompactResult, error) {
	t.Helper()
	d := auxStoreCompactDefEvery(aux, nil, interval)
	decode, task := t6g1Runner(t, d)
	p, err := decode(nil)
	require.NoError(t, err)
	h := newAuxHost(t.Context())
	raw, err := task.Run(tasks.NewRC(h), p)
	var res auxStoreCompactResult
	if err == nil {
		require.NoError(t, json.Unmarshal(raw, &res))
	}
	return h, res, err
}

func TestAuxStoreCompact_Conformance(t *testing.T) {
	taskstest.Conformance(t, auxStoreCompactDef(&auxStub{}, nil))
}

func TestAuxStoreCompact_Declaration(t *testing.T) {
	d := auxStoreCompactDef(&auxStub{}, nil)
	assert.Equal(t, tasks.Kind("books.maintenance.aux-store-compact"), d.Kind())
	assert.Empty(t, tasks.Problems(d))
	c := tasks.CommonOf(d)
	assert.Equal(t, []entity.Ref{tasks.Res("books", "aux_stores")}, c.Effects.WriteSet())
	assert.Empty(t, c.Effects.ReadSet())
	assert.Empty(t, c.Effects.DeleteSet())
	assert.Equal(t, tasks.ResumeDrop, c.Resume)
	assert.Equal(t, tasks.PriorityLow, c.Priority)
	assert.Equal(t, 2*time.Hour, c.Timeout)
	assert.Equal(t, t6g1StartRequirement().Permissions(), c.Perms.Start.Permissions())
	assert.Equal(t, []tasks.ReadySignal{t6g1SignalMemDB}, c.Ready)
	assert.Nil(t, c.Schedule, "no schedule passed, none declared")

	got := tasks.CommonOf(auxStoreCompactDef(&auxStub{}, tasks.Every(24*time.Hour))).Schedule
	require.NotNil(t, got, "the books key's schedule is the one the kind carries")
	assert.Equal(t, 24*time.Hour, got.Interval())
}

func TestAuxStoreCompact_NilOptimizerFailsClosed(t *testing.T) {
	assert.NotEmpty(t, tasks.Problems(auxStoreCompactDef(nil, nil)))
}

// HEAD's steps 2 and 3: an owned store is compacted, and the progress index
// and "(n/m)" labels are the native sequence's own.
func TestAuxStoreCompact_CoversHEADSteps2And3(t *testing.T) {
	aux := &auxStub{aiOwned: true, olOwned: true}
	h, res, err := runAux(t, aux, testTick)
	require.NoError(t, err)
	ai, ol := aux.calls()
	assert.Equal(t, 1, ai)
	assert.Equal(t, 1, ol)
	assert.Equal(t, auxStoreCompactResult{Optimized: 2}, res)

	notes, lines, _, current := h.snapshot()
	assert.Contains(t, notes, "Optimizing AI scan database...")
	assert.Contains(t, notes, "Optimizing OpenLibrary cache...")
	assert.Equal(t, []int64{0, 1}, current[:2], "the native sequence is zero-based over two steps")
	joined := strings.Join(append(notes, lines...), "\n")
	assert.Contains(t, joined, "Compacting AI scan database (1/2)")
	assert.Contains(t, joined, "Compacting OpenLibrary cache (2/2)")
}

// A store with no database of its own is still asked (HEAD's no-op Optimize,
// which counts as optimized), and an unwired OpenLibrary cache is skipped
// without being asked.
func TestAuxStoreCompact_SharedAndUnwiredStores(t *testing.T) {
	aux := &auxStub{aiOwned: false, olOwned: false}
	h, res, err := runAux(t, aux, testTick)
	require.NoError(t, err)
	ai, ol := aux.calls()
	assert.Equal(t, 1, ai, "a shared AI scan store is asked: Optimize is its no-op")
	assert.Equal(t, 0, ol, "an unwired OpenLibrary cache is not asked")
	assert.Equal(t, auxStoreCompactResult{Optimized: 1}, res)
	_, lines, _, _ := h.snapshot()
	joined := strings.Join(lines, "\n")
	assert.Contains(t, joined, "nothing separate to compact")
	assert.Contains(t, joined, "OpenLibrary cache not initialized, skipping")
}

// HEAD swallowed every store failure and finished clean. The native run tries
// both stores, then fails, naming each store that failed.
func TestAuxStoreCompact_FailureStillTriesBothStoresThenFails(t *testing.T) {
	aux := &auxStub{aiOwned: true, olOwned: true, aiErr: errors.New("batcave flooded")}
	_, _, err := runAux(t, aux, testTick)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "AI scan store")
	assert.Contains(t, err.Error(), "batcave flooded")
	ai, ol := aux.calls()
	assert.Equal(t, 1, ai)
	assert.Equal(t, 1, ol, "the second store is compacted although the first failed")

	both := &auxStub{aiOwned: true, olOwned: true, aiErr: errors.New("one"), olErr: errors.New("two")}
	_, _, err = runAux(t, both, testTick)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "one")
	assert.Contains(t, err.Error(), "two")
}

func TestAuxStoreCompact_SharedStoreFailureFails(t *testing.T) {
	aux := &auxStub{aiOwned: false, aiErr: errors.New("gotham")}
	_, _, err := runAux(t, aux, testTick)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gotham")
}

// The heartbeat contract, two-sided, for the native kind: counters moved ->
// a status line that stamps liveness; counters flat -> none. The fixed
// "Optimizing ..." lines before each store are the step boundaries.
func TestAuxStoreCompact_ReportsOnlyWhenCountersMove(t *testing.T) {
	moving := &auxStub{aiOwned: true, aiStatsF: debtFalling(), blockFor: 150 * time.Millisecond}
	h, _, err := runAux(t, moving, testTick)
	require.NoError(t, err)
	notes, _, _, _ := h.snapshot()
	var compacting int
	for _, n := range notes {
		if strings.HasPrefix(n, "Compacting AI scan database (1/2): ") {
			compacting++
		}
	}
	assert.Positive(t, compacting, "a store whose debt is falling must stamp liveness; notes: %v", notes)

	flat := &auxStub{aiOwned: true, aiStatsF: debtFlat(), blockFor: 150 * time.Millisecond}
	h, _, err = runAux(t, flat, testTick)
	require.NoError(t, err)
	notes, lines, touches, _ := h.snapshot()
	for _, n := range notes {
		assert.False(t, strings.HasPrefix(n, "Compacting "), "a flat engine stamped liveness: %q", n)
	}
	assert.Zero(t, touches, "a flat engine stamped liveness with bare touches")
	var ticks int
	for _, l := range lines {
		if strings.Contains(l, "elapsed") {
			ticks++
		}
	}
	assert.Positive(t, ticks, "the sampler still logs every tick; logs do not stamp liveness")
}

func TestAuxStoreCompact_CancelStopsTheRun(t *testing.T) {
	aux := &auxStub{aiOwned: true, olOwned: true, blockFor: time.Minute}
	d := auxStoreCompactDefEvery(aux, nil, testTick)
	decode, task := t6g1Runner(t, d)
	p, err := decode(nil)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	h := newAuxHost(ctx)
	done := make(chan error, 1)
	go func() { _, err := task.Run(tasks.NewRC(h), p); done <- err }()
	require.Eventually(t, func() bool { ai, _ := aux.calls(); return ai == 1 }, 2*time.Second, testTick)
	cancel()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("a canceled run kept compacting")
	}
}

// HEAD's definition runs the same core: the labels are "(2/3)" and "(3/3)",
// progress indices 1 and 2 of 3, and a failed store is not fatal -- the final
// line counts what succeeded, as it always did.
func TestAuxStoreCompact_HEADShellKeepsItsThreeStepSequence(t *testing.T) {
	rep := &seqReporter{}
	main := &compactStub{release: make(chan struct{})}
	close(main.release)
	aux := &auxStub{aiOwned: true, olOwned: true, olErr: errors.New("joker")}
	runDBOptimizeSteps(t.Context(), main, aux, rep, testTick)

	got := rep.frames()
	assert.Contains(t, got, frame{1, 3, "Optimizing AI scan database..."})
	assert.Contains(t, got, frame{2, 3, "Optimizing OpenLibrary cache..."})
	assert.Equal(t, frame{3, 3, "Database optimization complete: 2/3 stores in "}, trimTail(got[len(got)-1]),
		"the main store and the AI scan store succeeded; the OpenLibrary failure is counted out, not raised")
	logged := strings.Join(rep.logged(), "\n")
	assert.Contains(t, logged, "Compacting AI scan database (2/3)")
	assert.Contains(t, logged, "Compacting OpenLibrary cache (3/3)")
}

type frame struct {
	cur, total int
	msg        string
}

// trimTail drops the elapsed time from the completion line.
func trimTail(f frame) frame {
	if i := strings.Index(f.msg, " in "); i >= 0 {
		f.msg = f.msg[:i+len(" in ")]
	}
	return f
}

// seqReporter records every UpdateProgress (with its indices) and every Log.
type seqReporter struct {
	heartbeatReporter
	fmu sync.Mutex
	fs  []frame
	ls  []string
}

func (r *seqReporter) UpdateProgress(cur, total int, msg string) error {
	r.fmu.Lock()
	r.fs = append(r.fs, frame{cur, total, msg})
	r.fmu.Unlock()
	return nil
}

func (r *seqReporter) Log(_ slog.Level, msg string, _ ...slog.Attr) error {
	r.fmu.Lock()
	r.ls = append(r.ls, msg)
	r.fmu.Unlock()
	return nil
}

func (r *seqReporter) frames() []frame {
	r.fmu.Lock()
	defer r.fmu.Unlock()
	return append([]frame(nil), r.fs...)
}

func (r *seqReporter) logged() []string {
	r.fmu.Lock()
	defer r.fmu.Unlock()
	return append([]string(nil), r.ls...)
}

// The shared core's outcome is what both shells read: HEAD's shell takes the
// count, the native shell the failures.
func TestAuxStoreCompact_CoreOutcomeCountsStoresAndFailures(t *testing.T) {
	env := func() auxCompactEnv {
		return auxCompactEnv{Rep: &seqReporter{}, Frame: func(int, string) {}, First: 0, Total: 2, Interval: testTick, Log: slog.New(slog.DiscardHandler)}
	}
	ok := compactAuxStores(t.Context(), &auxStub{aiOwned: true, olOwned: true}, env())
	assert.Equal(t, 2, ok.Optimized)
	assert.Zero(t, ok.Failed)
	assert.Empty(t, ok.Errs)

	bad := compactAuxStores(t.Context(), &auxStub{aiOwned: true, olOwned: true, olErr: errors.New("riddler")}, env())
	assert.Equal(t, 1, bad.Optimized)
	assert.Equal(t, 1, bad.Failed)
	require.Len(t, bad.Errs, 1)
	assert.Contains(t, bad.Errs[0].Error(), "OpenLibrary cache")
}

// ReportUpdate is Progress.Update: the position, then the status line when
// there is one, recorded the way ReportProgress and Note record them.
func (h *auxHost) ReportUpdate(done int64, total tasks.Total, msg string) {
	h.ReportProgress(done, total)
	if msg != "" {
		h.Note(msg)
	}
}
