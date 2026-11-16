// file: internal/plugins/maintenance/optimize_native_test.go
// version: 1.3.3
// guid: 9a1e4c52-3d78-4b06-8e2f-5c7d0b1a6f93
// last-edited: 2026-11-15

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/operations/childop"
	"github.com/falkcorp/audiobook-organizer/internal/serverdecode"
)

// t6g6OptimizeHost is a run host whose children are scripted: Enqueue records the
// child kind and params, Await answers per child kind.
type t6g6OptimizeHost struct {
	*t6g6Host

	mu       sync.Mutex
	enq      []tasks.Kind
	params   []any
	failKind map[tasks.Kind]bool
	deadline time.Time
	n        int

	// enqErr makes Enqueue of that kind fail; onEnq runs as Enqueue of a kind is
	// called (before it answers), so a test can snapshot the checkpoint as it
	// stood then; block parks Await of that kind until the channel is closed;
	// awaited records every run id awaited.
	enqErr  map[tasks.Kind]error
	onEnq   func(tasks.Kind)
	onAwait func(tasks.RunID)
	block   map[tasks.Kind]chan struct{}
	awaited []tasks.RunID

	// touches counts liveness stamps (rc.Progress().Tick); reported is every
	// done value the run reported (rc.Progress().Set), in order.
	touches  atomic.Int64
	reported []int64
}

func (h *t6g6OptimizeHost) Touch() { h.touches.Add(1) }

func (h *t6g6OptimizeHost) ReportProgress(done int64, _ tasks.Total) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reported = append(h.reported, done)
}

// lastReported is the last done value the run reported (what the kernel would
// hold as the run's Done when the attempt ends), or 0.
func (h *t6g6OptimizeHost) lastReported() int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.reported) == 0 {
		return 0
	}
	return h.reported[len(h.reported)-1]
}

func (h *t6g6OptimizeHost) Enqueue(k tasks.Kind, params any, _ ...tasks.StartOption) (tasks.RunRef, error) {
	if h.onEnq != nil {
		h.onEnq(k)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.enqErr[k]; err != nil {
		return tasks.RunRef{}, err
	}
	h.enq = append(h.enq, k)
	h.params = append(h.params, params)
	h.n++
	return tasks.RunRef{ID: tasks.RunID(string(k) + "#" + strconv.Itoa(h.n))}, nil
}

func (h *t6g6OptimizeHost) Await(ref tasks.RunRef) (tasks.Outcome, error) {
	if h.onAwait != nil {
		h.onAwait(ref.ID)
	}
	h.mu.Lock()
	h.awaited = append(h.awaited, ref.ID)
	var blk chan struct{}
	failed := false
	for k, c := range h.block {
		if strings.HasPrefix(string(ref.ID), string(k)) {
			blk = c
		}
	}
	for k := range h.failKind {
		if strings.HasPrefix(string(ref.ID), string(k)) {
			failed = true
		}
	}
	h.mu.Unlock()
	if blk != nil {
		<-blk
	}
	if failed {
		return tasks.Outcome{State: tasks.StateFailed, Err: "child failed"}, nil
	}
	return tasks.Outcome{State: tasks.StateSucceeded}, nil
}

func (h *t6g6OptimizeHost) Deadline() time.Time { return h.deadline }

func (h *t6g6OptimizeHost) kinds() []tasks.Kind {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]tasks.Kind(nil), h.enq...)
}

func t6g6RunOptimize(t *testing.T, h *t6g6OptimizeHost) error {
	t.Helper()
	p := newT6G6Probe(t)
	def := t6g6ByKind(t, (&Plugin{}).t6g6Kinds(p.deps(), nil), optimizeKind)
	r := def.(interface {
		ExecutorRunners() (func(json.RawMessage) (any, error), *tasks.TaskSpec[any, json.RawMessage], *tasks.BatchSpec[any, any, json.RawMessage], *tasks.FixerSpec[any, any])
	})
	decode, task, _, _ := r.ExecutorRunners()
	in, err := decode(nil)
	require.NoError(t, err)
	_, err = task.Run(tasks.NewRC(h), in)
	return err
}

func t6g6OptimizeFlags(t *testing.T, backfill, decode bool) {
	t.Helper()
	old := config.AppConfig.Maintenance.AcoustIDBackfill
	t.Cleanup(func() { config.AppConfig.Maintenance.AcoustIDBackfill = old })
	config.AppConfig.Maintenance.AcoustIDBackfill = backfill
	t.Setenv(serverdecode.EnvVar, strconv.FormatBool(decode))
}

func newT6G6OptimizeHost(t *testing.T) *t6g6OptimizeHost {
	t.Helper()
	return &t6g6OptimizeHost{
		t6g6Host: &t6g6Host{FakeRun: taskstest.NewFakeRun(context.Background(), optimizeKind), mode: tasks.ModeLive},
		failKind: map[tasks.Kind]bool{},
		enqErr:   map[tasks.Kind]error{},
		block:    map[tasks.Kind]chan struct{}{},
	}
}

// The sweep starts its four children in order, by the kinds the acoustid and
// maintenance rows declare (a literal list: no row on the stack declares the
// acoustid kinds yet, so this is the pin they must match).
func TestT6G6Optimize_ChildrenInOrder(t *testing.T) {
	t6g6OptimizeFlags(t, true, true)
	h := newT6G6OptimizeHost(t)
	require.NoError(t, t6g6RunOptimize(t, h))
	require.Equal(t, []tasks.Kind{
		"books.maintenance.temp-file-cleanup",
		"books.acoustid.fingerprint-rescan",
		"books.acoustid.scan",
		"books.acoustid.backfill",
	}, h.kinds())
	require.Equal(t, map[string]any{"scope": "missing"}, h.params[1])
}

// The two gates drop their child up front.
func TestT6G6Optimize_GatedChildrenDropped(t *testing.T) {
	t6g6OptimizeFlags(t, false, false)
	h := newT6G6OptimizeHost(t)
	require.NoError(t, t6g6RunOptimize(t, h))
	require.Equal(t, []tasks.Kind{"books.maintenance.temp-file-cleanup", "books.acoustid.scan"}, h.kinds())
}

// A failed child is counted and the sweep carries on, as HEAD did.
func TestT6G6Optimize_FailedChildDoesNotStopSweep(t *testing.T) {
	t6g6OptimizeFlags(t, false, false)
	h := newT6G6OptimizeHost(t)
	h.failKind["books.maintenance.temp-file-cleanup"] = true
	require.NoError(t, t6g6RunOptimize(t, h))
	require.Len(t, h.kinds(), 2)
}

// checkpointOf decodes the sweep checkpoint a host holds.
func t6g6OptimizeCheckpointOf(t *testing.T, h *t6g6OptimizeHost) (optimizeCheckpoint, json.RawMessage) {
	t.Helper()
	raw, ok := h.CheckpointJSON("")
	require.True(t, ok, "a checkpoint was saved")
	var cp optimizeCheckpoint
	require.NoError(t, json.Unmarshal(raw, &cp))
	return cp, raw
}

// Before it starts a child with the attempt's deadline inside the yield margin,
// the sweep checkpoints and yields, but only after a child finished in this
// attempt (or the resumed attempt would make no progress). The core is driven
// directly so the clock is a plain value.
func TestT6G6Optimize_YieldsBeforeStartingAChildNearTheDeadline(t *testing.T) {
	t6g6OptimizeFlags(t, true, true)
	var enq []string
	var saved []optimizeCheckpoint
	env := optimizeEnv{
		t6g6Env:  t6g6HeadEnv(&fakeReporter{}, true),
		Deadline: func() time.Time { return time.Now().Add(time.Minute) },
		Enqueue: func(_ context.Context, defID string, _ any) (string, error) {
			enq = append(enq, defID)
			return defID + "#run", nil
		},
		Wait: func(context.Context, string, func(childop.Observation)) error { return nil },
	}
	env.Checkpoint = func(v any) error { saved = append(saved, v.(optimizeCheckpoint)); return nil }
	env.Restore = func(any) (bool, error) { return false, nil }
	err := optimizeCore(context.Background(), env)
	require.ErrorIs(t, err, tasks.ErrContinueInPlace)
	require.Equal(t, []string{optimizeChildTempFileCleanup}, enq, "the first child ran, the second did not start")
	last := saved[len(saved)-1]
	require.Equal(t, []string{"temp-file-cleanup"}, last.Finished)
	require.Nil(t, last.InFlight)
}

// A child still running when the attempt nears its deadline is yielded on, not
// timed out with: the checkpoint names the in-flight run, the child is not
// launched again, and the resumed attempt re-attaches to that run, then runs the
// children left.
func TestT6G6Optimize_ChildOutlivesDeadlineYieldsAndResumeReattaches(t *testing.T) {
	t6g6OptimizeFlags(t, true, true)
	h := newT6G6OptimizeHost(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	h.block["books.maintenance.temp-file-cleanup"] = release
	h.deadline = time.Now().Add(optimizeYieldMargin + 150*time.Millisecond)

	err := t6g6RunOptimize(t, h)
	require.ErrorIs(t, err, tasks.ErrContinueInPlace)
	require.Equal(t, []tasks.Kind{"books.maintenance.temp-file-cleanup"}, h.kinds(), "the child was launched once")
	cp, raw := t6g6OptimizeCheckpointOf(t, h)
	require.Empty(t, cp.Finished, "the running child is not finished")
	require.NotNil(t, cp.InFlight)
	require.Equal(t, "temp-file-cleanup", cp.InFlight.Name)
	require.Equal(t, "books.maintenance.temp-file-cleanup#1", cp.InFlight.RunID)
	require.False(t, cp.StartedAt.IsZero())
	// The yield carries progress the kernel accepts: no child finished in this
	// attempt, yet the run's reported done moved past its start (0).
	require.Greater(t, h.lastReported(), int64(0))
	require.Equal(t, int64(cp.Steps), h.lastReported(), "the checkpointed steps are the reported done")

	// Resume: the child has finished in between; the resumed attempt waits on the
	// same run and starts only the three children left.
	h2 := newT6G6OptimizeHost(t)
	require.NoError(t, h2.SetCheckpoint("", json.RawMessage(raw)))
	require.NoError(t, t6g6RunOptimize(t, h2))
	require.Equal(t, []tasks.Kind{
		"books.acoustid.fingerprint-rescan", "books.acoustid.scan", "books.acoustid.backfill",
	}, h2.kinds(), "the in-flight child was not launched a second time")
	require.Equal(t, tasks.RunID("books.maintenance.temp-file-cleanup#1"), h2.awaited[0])
}

// An attempt that re-attaches to the child an earlier attempt left in flight
// checkpoints at once, before it waits, and the checkpoint carries the re-attach
// step. The seeded checkpoint already names the run, so the run id alone cannot
// tell a fresh save from the seed: the step count is what moved. Without the save
// a re-attach that waits out its whole span is struck as uncheckpointed, and a
// crash while it waits resumes from the seed's step count, so the kernel could
// see the next attempt report no progress past the one before.
func TestT6G6Optimize_ReattachCheckpointsBeforeTheChildEnds(t *testing.T) {
	t6g6OptimizeFlags(t, false, false) // the list is cleanup, scan
	const runID = "books.maintenance.temp-file-cleanup#9"
	seed, err := json.Marshal(optimizeCheckpoint{
		StartedAt: time.Now().Add(-time.Hour),
		InFlight:  &optimizeInFlight{Name: "temp-file-cleanup", RunID: runID},
		Steps:     1,
	})
	require.NoError(t, err)
	h := newT6G6OptimizeHost(t)
	require.NoError(t, h.SetCheckpoint("", json.RawMessage(seed)))
	var atAwait json.RawMessage
	h.onAwait = func(id tasks.RunID) {
		if atAwait == nil {
			atAwait, _ = h.CheckpointJSON("") // what a crash while the child runs would resume from
		}
	}
	require.NoError(t, t6g6RunOptimize(t, h))
	require.Equal(t, tasks.RunID(runID), h.awaited[0], "the recorded run is the one re-attached to")
	require.NotNil(t, atAwait, "the checkpoint was read as the wait began")
	var cp optimizeCheckpoint
	require.NoError(t, json.Unmarshal(atAwait, &cp))
	require.NotNil(t, cp.InFlight)
	require.Equal(t, runID, cp.InFlight.RunID)
	require.Empty(t, cp.Finished, "the child has not ended yet")
	require.Equal(t, 2, cp.Steps, "the re-attach was checkpointed with its step (the seed held 1)")
}

// A crash after child k leaves the checkpoint the sweep wrote at that boundary,
// and a resumed attempt starts at child k+1 rather than re-running the sweep.
func TestT6G6Optimize_CrashAfterAChildResumesAtTheNext(t *testing.T) {
	t6g6OptimizeFlags(t, true, true)
	h := newT6G6OptimizeHost(t)
	var snap json.RawMessage
	h.onEnq = func(k tasks.Kind) {
		if k == "books.acoustid.fingerprint-rescan" && snap == nil {
			snap, _ = h.CheckpointJSON("")
		}
	}
	var awaiting json.RawMessage
	var awaitedID tasks.RunID
	h.onAwait = func(id tasks.RunID) {
		if awaiting == nil {
			awaiting, _ = h.CheckpointJSON("")
			awaitedID = id
		}
	}
	require.NoError(t, t6g6RunOptimize(t, h))
	// The checkpoint already names the child's run while it is awaited, so a crash
	// there re-attaches instead of launching it again.
	var mid optimizeCheckpoint
	require.NoError(t, json.Unmarshal(awaiting, &mid))
	require.NotNil(t, mid.InFlight)
	require.Equal(t, string(awaitedID), mid.InFlight.RunID)
	require.NotNil(t, snap, "a checkpoint existed before the second child started")
	var cp optimizeCheckpoint
	require.NoError(t, json.Unmarshal(snap, &cp))
	require.Equal(t, []string{"temp-file-cleanup"}, cp.Finished)
	require.Equal(t, 1, cp.Completed)
	require.Nil(t, cp.InFlight)

	h2 := newT6G6OptimizeHost(t)
	require.NoError(t, h2.SetCheckpoint("", snap))
	require.NoError(t, t6g6RunOptimize(t, h2))
	require.Equal(t, []tasks.Kind{
		"books.acoustid.fingerprint-rescan", "books.acoustid.scan", "books.acoustid.backfill",
	}, h2.kinds())
}

// fakeSweepClock is the sweep's time on a clock the test advances by hand.
// withDeadline returns a context that ends with context.DeadlineExceeded (its
// Err and its cause, as a real deadline does) when advance reaches the deadline,
// and never on its own.
type fakeSweepClock struct {
	mu      sync.Mutex
	t       time.Time
	waiters []*fakeSweepWaiter
}

type fakeSweepWaiter struct {
	at     time.Time
	expire func()
}

type fakeDeadlineCtx struct {
	context.Context
	at      time.Time
	expired atomic.Bool
}

func (c *fakeDeadlineCtx) Deadline() (time.Time, bool) { return c.at, true }

func (c *fakeDeadlineCtx) Err() error {
	if c.expired.Load() {
		return context.DeadlineExceeded
	}
	return c.Context.Err()
}

func (c *fakeSweepClock) clock() optimizeClock {
	return optimizeClock{
		Now: func() time.Time {
			c.mu.Lock()
			defer c.mu.Unlock()
			return c.t
		},
		WithDeadline: func(ctx context.Context, at time.Time) (context.Context, context.CancelFunc) {
			inner, cancel := context.WithCancelCause(ctx)
			fc := &fakeDeadlineCtx{Context: inner, at: at}
			expire := func() {
				fc.expired.Store(true)
				cancel(context.DeadlineExceeded)
			}
			c.mu.Lock()
			if !at.After(c.t) {
				c.mu.Unlock()
				expire()
			} else {
				c.waiters = append(c.waiters, &fakeSweepWaiter{at: at, expire: expire})
				c.mu.Unlock()
			}
			return fc, func() { cancel(context.Canceled) }
		},
	}
}

// advance moves the clock forward and expires every deadline it reached.
func (c *fakeSweepClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	var due []func()
	kept := c.waiters[:0]
	for _, w := range c.waiters {
		if w.at.After(c.t) {
			kept = append(kept, w)
		} else {
			due = append(due, w.expire)
		}
	}
	c.waiters = kept
	c.mu.Unlock()
	for _, f := range due {
		f()
	}
}

// HEAD's 36h budget is kept across attempts: a checkpoint whose sweep began over
// the budget ago fails before any child starts; one that runs out while a child
// is still running fails instead of yielding; and a checkpoint with no start time
// is corrupt, not a fresh budget.
func TestT6G6Optimize_BudgetIsHeldAtEveryBoundary(t *testing.T) {
	t6g6OptimizeFlags(t, true, true)
	// The budget is measured on a clock this test advances, so "the budget ends
	// 2 s into the wait" is an event the test fires, not a race with the wall
	// clock.
	clk := &fakeSweepClock{t: time.Date(2026, time.November, 15, 12, 0, 0, 0, time.UTC)}
	origClock := optimizeSweepClock
	optimizeSweepClock = clk.clock()
	t.Cleanup(func() { optimizeSweepClock = origClock })
	seed := func(h *t6g6OptimizeHost, cp optimizeCheckpoint) {
		raw, err := json.Marshal(cp)
		require.NoError(t, err)
		require.NoError(t, h.SetCheckpoint("", json.RawMessage(raw)))
	}

	h := newT6G6OptimizeHost(t)
	seed(h, optimizeCheckpoint{StartedAt: clk.clock().Now().Add(-37 * time.Hour), Finished: []string{"temp-file-cleanup"}})
	err := t6g6RunOptimize(t, h)
	require.Error(t, err)
	require.Contains(t, err.Error(), "budget")
	require.Empty(t, h.kinds())

	// Mid-sweep: the budget has 2 s left when the second child's wait begins, and
	// the clock is advanced past it from inside that wait.
	h = newT6G6OptimizeHost(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	h.block["books.acoustid.fingerprint-rescan"] = release
	h.onAwait = func(tasks.RunID) { clk.advance(3 * time.Second) }
	seed(h, optimizeCheckpoint{StartedAt: clk.clock().Now().Add(-optimizeTotalBudget + 2*time.Second), Finished: []string{"temp-file-cleanup"}, Completed: 1})
	err = t6g6RunOptimize(t, h)
	require.Error(t, err)
	require.NotErrorIs(t, err, tasks.ErrContinueInPlace)
	require.Contains(t, err.Error(), "budget")
	require.Equal(t, []tasks.Kind{"books.acoustid.fingerprint-rescan"}, h.kinds(), "no child started after the budget ran out")

	h = newT6G6OptimizeHost(t)
	seed(h, optimizeCheckpoint{Finished: []string{"temp-file-cleanup"}})
	err = t6g6RunOptimize(t, h)
	require.Error(t, err)
	require.Contains(t, err.Error(), "start time")
	require.Empty(t, h.kinds())
}

// A child whose enqueue fails is counted and recorded as finished at once, so a
// resume does not retry it and the sweep goes on.
func TestT6G6Optimize_EnqueueFailureIsRecordedAndSweepContinues(t *testing.T) {
	t6g6OptimizeFlags(t, false, false)
	h := newT6G6OptimizeHost(t)
	h.enqErr["books.acoustid.scan"] = errors.New("queue is full")
	require.NoError(t, t6g6RunOptimize(t, h))
	require.Equal(t, []tasks.Kind{"books.maintenance.temp-file-cleanup"}, h.kinds())
	cp, _ := t6g6OptimizeCheckpointOf(t, h)
	require.Equal(t, []string{"temp-file-cleanup", "acoustid-scan"}, cp.Finished)
	require.Equal(t, 1, cp.Completed)
	require.Equal(t, 1, cp.Failed)
}

// A resumed attempt carries on from a checkpoint under the original budget.
func TestT6G6Optimize_ResumesFromACheckpoint(t *testing.T) {
	t6g6OptimizeFlags(t, true, true)
	raw, err := json.Marshal(optimizeCheckpoint{StartedAt: time.Now().Add(-time.Hour), Finished: []string{"temp-file-cleanup"}, Completed: 1})
	require.NoError(t, err)
	h := newT6G6OptimizeHost(t)
	require.NoError(t, h.SetCheckpoint("", json.RawMessage(raw)))
	require.NoError(t, t6g6RunOptimize(t, h))
	require.Equal(t, []tasks.Kind{
		"books.acoustid.fingerprint-rescan", "books.acoustid.scan", "books.acoustid.backfill",
	}, h.kinds())
}

// A Preview run is refused before any child starts.
func TestT6G6Optimize_PreviewRefused(t *testing.T) {
	t6g6OptimizeFlags(t, true, true)
	h := newT6G6OptimizeHost(t)
	h.mode = tasks.ModePreview
	require.Error(t, t6g6RunOptimize(t, h))
	require.Empty(t, h.kinds())
}

// The native sweep stamps no liveness of its own while it waits on a child: the
// kernel's await relays it to the waiting run while the child is alive and,
// deliberately, not while the child waits for a restart or a person. A tick of
// the sweep's own would override that rule, so the wait must go through rc.Await
// and touch nothing. (That the kernel's await really keeps the parent alive past
// its watchdog limit is core's contract, pinned by core's
// TestAwait_ParentSurvivesChildLongerThanItsWatchdogLimit; the host here stands
// in for the kernel and relays nothing, so a stamp counted here is the row's.)
// The assertion is direct: no timer and no timeout stands between a regression
// and a failure.
func TestT6G6Optimize_WaitRidesOnTheKernelsAwaitAndStampsNothingItself(t *testing.T) {
	t6g6OptimizeFlags(t, false, false)
	h := newT6G6OptimizeHost(t)
	release := make(chan struct{})
	h.block["books.maintenance.temp-file-cleanup"] = release
	awaiting := make(chan struct{})
	h.onAwait = func(id tasks.RunID) {
		if strings.HasPrefix(string(id), "books.maintenance.temp-file-cleanup") {
			close(awaiting)
		}
	}
	res := make(chan error, 1)
	go func() { res <- t6g6RunOptimize(t, h) }()
	<-awaiting // the sweep is blocked in rc.Await on its child
	require.Zero(t, h.touches.Load(), "the wait stamps no liveness while the child runs")
	close(release)
	require.NoError(t, <-res)
	require.Zero(t, h.touches.Load(), "the sweep stamped no liveness at all")
}

// A yield must carry progress the kernel accepts (done past where the attempt
// began), also when the attempt finished no child: here the first attempt waits
// on a child past its deadline, and so does the one that re-attaches to it.
func TestT6G6Optimize_YieldWithNoChildFinishedStillReportsProgress(t *testing.T) {
	t6g6OptimizeFlags(t, true, true)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	yield := func(h *t6g6OptimizeHost) {
		h.block["books.maintenance.temp-file-cleanup"] = release
		h.deadline = time.Now().Add(optimizeYieldMargin + 150*time.Millisecond)
	}

	h := newT6G6OptimizeHost(t)
	yield(h)
	require.ErrorIs(t, t6g6RunOptimize(t, h), tasks.ErrContinueInPlace)
	first := h.lastReported()
	require.Greater(t, first, int64(0), "progress past the run's start (0)")
	_, raw := t6g6OptimizeCheckpointOf(t, h)

	h2 := newT6G6OptimizeHost(t)
	yield(h2)
	require.NoError(t, h2.SetCheckpoint("", json.RawMessage(raw)))
	require.ErrorIs(t, t6g6RunOptimize(t, h2), tasks.ErrContinueInPlace)
	require.Greater(t, h2.lastReported(), first, "the re-attached attempt reports past where the one before ended")
	h2.mu.Lock()
	awaited := append([]tasks.RunID(nil), h2.awaited...)
	h2.mu.Unlock()
	require.Equal(t, []tasks.RunID{"books.maintenance.temp-file-cleanup#1"}, awaited)
	require.Empty(t, h2.kinds(), "nothing was launched a second time")
	cp, _ := t6g6OptimizeCheckpointOf(t, h2)
	require.NotNil(t, cp.InFlight, "the child is still recorded in flight")
}

// A child that was in flight when a flag change took it off the list is still
// waited on, ahead of the rest, and counted; its run is never left beside the
// children that start after it.
func TestT6G6Optimize_InFlightChildNoLongerListedIsWaitedOn(t *testing.T) {
	t6g6OptimizeFlags(t, false, false) // acoustid-backfill is off today
	raw, err := json.Marshal(optimizeCheckpoint{
		StartedAt: time.Now().Add(-time.Hour),
		InFlight:  &optimizeInFlight{Name: "acoustid-backfill", RunID: "books.acoustid.backfill#9"},
	})
	require.NoError(t, err)
	h := newT6G6OptimizeHost(t)
	require.NoError(t, h.SetCheckpoint("", json.RawMessage(raw)))
	require.NoError(t, t6g6RunOptimize(t, h))
	require.Equal(t, tasks.RunID("books.acoustid.backfill#9"), h.awaited[0], "the recorded run is waited on first")
	require.Equal(t, []tasks.Kind{"books.maintenance.temp-file-cleanup", "books.acoustid.scan"}, h.kinds(),
		"only today's children are launched, and not the dropped one")
	cp, _ := t6g6OptimizeCheckpointOf(t, h)
	require.Equal(t, []string{"acoustid-backfill", "temp-file-cleanup", "acoustid-scan"}, cp.Finished)
	require.Equal(t, 3, cp.Completed)
	require.Nil(t, cp.InFlight)
}

// A child that fails after it was waited on is recorded at once: a crash right
// after it resumes at the next child, not at the failed one.
func TestT6G6Optimize_FailedChildIsCheckpointedAtOnce(t *testing.T) {
	t6g6OptimizeFlags(t, true, true)
	h := newT6G6OptimizeHost(t)
	h.failKind["books.maintenance.temp-file-cleanup"] = true
	var snap json.RawMessage
	h.onEnq = func(k tasks.Kind) {
		if k == "books.acoustid.fingerprint-rescan" {
			snap, _ = h.CheckpointJSON("")
		}
	}
	require.NoError(t, t6g6RunOptimize(t, h))
	require.NotNil(t, snap)
	var cp optimizeCheckpoint
	require.NoError(t, json.Unmarshal(snap, &cp))
	require.Equal(t, []string{"temp-file-cleanup"}, cp.Finished)
	require.Equal(t, 1, cp.Failed)
	require.Equal(t, 0, cp.Completed)
	require.Nil(t, cp.InFlight)
}

// The kernel's repeat-start dedupe folds a start with identical params into the
// run already there, so a resumed sweep whose in-flight record was lost (the
// checkpoint flushes at most every 30s) must start the child with params
// identical to the first start's.
func TestT6G6Optimize_LostInFlightRecordRestartsTheChildWithIdenticalParams(t *testing.T) {
	t6g6OptimizeFlags(t, true, true)
	first := newT6G6OptimizeHost(t)
	require.NoError(t, t6g6RunOptimize(t, first))
	want := map[tasks.Kind]any{}
	for i, k := range first.kinds() {
		want[k] = first.params[i]
	}

	// The crash lost the record of fingerprint-rescan, started but not recorded.
	raw, err := json.Marshal(optimizeCheckpoint{
		StartedAt: time.Now().Add(-time.Hour), Finished: []string{"temp-file-cleanup"}, Completed: 1,
	})
	require.NoError(t, err)
	h := newT6G6OptimizeHost(t)
	require.NoError(t, h.SetCheckpoint("", json.RawMessage(raw)))
	require.NoError(t, t6g6RunOptimize(t, h))
	require.Equal(t, "books.acoustid.fingerprint-rescan", string(h.kinds()[0]))
	for i, k := range h.kinds() {
		require.Equal(t, want[k], h.params[i], "params of %s", k)
	}
}

// The recorded in-flight child goes first even when today's list still holds
// it behind a child a restart newly enabled: with ALLOW_SERVER_DECODE turned on,
// fingerprint-rescan sits ahead of the re-queued acoustid-backfill, and it must
// not start beside it.
func TestT6G6Optimize_InFlightChildIsWaitedOnBeforeANewlyEnabledOne(t *testing.T) {
	t6g6OptimizeFlags(t, true, true) // the list is cleanup, rescan, scan, backfill
	raw, err := json.Marshal(optimizeCheckpoint{
		StartedAt: time.Now().Add(-time.Hour),
		Finished:  []string{"temp-file-cleanup"},
		InFlight:  &optimizeInFlight{Name: "acoustid-backfill", RunID: "books.acoustid.backfill#9"},
	})
	require.NoError(t, err)
	h := newT6G6OptimizeHost(t)
	require.NoError(t, h.SetCheckpoint("", json.RawMessage(raw)))
	var enqueuedBeforeAwait []tasks.Kind
	h.onAwait = func(id tasks.RunID) {
		if id == "books.acoustid.backfill#9" && enqueuedBeforeAwait == nil {
			enqueuedBeforeAwait = h.kinds()
			if enqueuedBeforeAwait == nil {
				enqueuedBeforeAwait = []tasks.Kind{}
			}
		}
	}
	require.NoError(t, t6g6RunOptimize(t, h))
	require.Equal(t, tasks.RunID("books.acoustid.backfill#9"), h.awaited[0], "the recorded run is waited on first")
	require.Empty(t, enqueuedBeforeAwait, "no child started before the in-flight one was awaited")
	require.Equal(t, []tasks.Kind{"books.acoustid.fingerprint-rescan", "books.acoustid.scan"}, h.kinds(),
		"the in-flight child is not launched again")
}
