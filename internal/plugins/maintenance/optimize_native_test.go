// file: internal/plugins/maintenance/optimize_native_test.go
// version: 1.0.1
// guid: 9a1e4c52-3d78-4b06-8e2f-5c7d0b1a6f93
// last-edited: 2026-11-14

package maintenance

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"

	"github.com/falkcorp/audiobook-organizer/internal/config"
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
}

func (h *t6g6OptimizeHost) Enqueue(k tasks.Kind, params any, _ ...tasks.StartOption) (tasks.RunRef, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.enq = append(h.enq, k)
	h.params = append(h.params, params)
	h.n++
	return tasks.RunRef{ID: tasks.RunID(string(k) + "#" + strconv.Itoa(h.n))}, nil
}

func (h *t6g6OptimizeHost) Await(ref tasks.RunRef) (tasks.Outcome, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for k := range h.failKind {
		if len(ref.ID) > len(k) && string(ref.ID)[:len(k)] == string(k) {
			return tasks.Outcome{State: tasks.StateFailed, Err: "child failed"}, nil
		}
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

// Near the attempt's deadline the sweep checkpoints and yields between children,
// and a resumed attempt runs only the children left, under the original budget.
func TestT6G6Optimize_YieldsNearDeadlineAndResumes(t *testing.T) {
	t6g6OptimizeFlags(t, true, true)
	h := newT6G6OptimizeHost(t)
	// The deadline is 1 minute out: inside the 5-minute margin from the start,
	// so the first child runs (nothing yields before one finished) and the
	// sweep then yields.
	h.deadline = time.Now().Add(time.Minute)
	err := t6g6RunOptimize(t, h)
	require.ErrorIs(t, err, tasks.ErrContinueInPlace)
	require.Equal(t, []tasks.Kind{"books.maintenance.temp-file-cleanup"}, h.kinds())
	raw, ok := h.CheckpointJSON("")
	require.True(t, ok, "a checkpoint was saved")
	require.Contains(t, string(raw), "temp-file-cleanup")

	// Resume on the same checkpoint with time to spare: the three left run.
	h2 := newT6G6OptimizeHost(t)
	require.NoError(t, h2.SetCheckpoint("", json.RawMessage(raw)))
	require.NoError(t, t6g6RunOptimize(t, h2))
	require.Equal(t, []tasks.Kind{
		"books.acoustid.fingerprint-rescan", "books.acoustid.scan", "books.acoustid.backfill",
	}, h2.kinds())

	// A checkpoint whose sweep began more than the 36h budget ago fails.
	h3 := newT6G6OptimizeHost(t)
	old, err := json.Marshal(optimizeCheckpoint{StartedAt: time.Now().Add(-37 * time.Hour), Finished: []string{"temp-file-cleanup"}})
	require.NoError(t, err)
	require.NoError(t, h3.SetCheckpoint("", json.RawMessage(old)))
	err = t6g6RunOptimize(t, h3)
	require.Error(t, err)
	require.Contains(t, err.Error(), "budget")
	require.Empty(t, h3.kinds())
}

// A Preview run is refused before any child starts.
func TestT6G6Optimize_PreviewRefused(t *testing.T) {
	t6g6OptimizeFlags(t, true, true)
	h := newT6G6OptimizeHost(t)
	h.mode = tasks.ModePreview
	require.Error(t, t6g6RunOptimize(t, h))
	require.Empty(t, h.kinds())
}
