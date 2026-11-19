// file: core/internal/executor/prune_test.go
// version: 1.0.1
// guid: e61178bc-35fc-4695-8a78-596683d7ace9
// last-edited: 2026-11-18

package executor

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"
)

// erasedTask is the executor's view of a definition (tasks.ExecutorRunners).
type erasedTask interface {
	ExecutorRunners() (decode func(json.RawMessage) (any, error),
		task *tasks.TaskSpec[any, json.RawMessage], batch *tasks.BatchSpec[any, any, json.RawMessage], fixer *tasks.FixerSpec[any, any])
}

func runPrune(t *testing.T, def tasks.Definition, host tasks.RunHost) (PruneResult, error) {
	t.Helper()
	e, ok := def.(erasedTask)
	if !ok {
		t.Fatalf("%T does not expose its runners", def)
	}
	decode, task, _, _ := e.ExecutorRunners()
	if task == nil || task.Run == nil {
		t.Fatalf("%s has no task body", def.Kind())
	}
	p, err := decode(nil)
	if err != nil {
		t.Fatalf("decode params: %v", err)
	}
	raw, err := task.Run(tasks.NewRC(host), p)
	var res PruneResult
	if raw != nil {
		if jerr := json.Unmarshal(raw, &res); jerr != nil {
			t.Fatalf("result %s: %v", raw, jerr)
		}
	}
	return res, err
}

// fakePruner serves a fixed number of old terminal runs, handing out at most
// limit per call, and records what it was asked.
type fakePruner struct {
	mu        sync.Mutex
	left      int
	calls     []pruneCall
	failAfter int // calls before an error; 0 = never
	err       error
	onCall    func(n int)
}

type pruneCall struct {
	before time.Time
	limit  int
}

func (f *fakePruner) Prune(_ context.Context, before time.Time, limit int) (int, error) {
	f.mu.Lock()
	f.calls = append(f.calls, pruneCall{before, limit})
	n := len(f.calls)
	hook := f.onCall
	f.mu.Unlock()
	if hook != nil {
		hook(n)
	}
	if f.failAfter > 0 && n > f.failAfter {
		return 0, f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	d := min(f.left, limit)
	f.left -= d
	return d, nil
}

func (f *fakePruner) snapshot() []pruneCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

var pruneNow = time.Date(2026, 11, 18, 3, 0, 0, 0, time.UTC)

func retention(d time.Duration) func() time.Duration { return func() time.Duration { return d } }

func pruneDef(p RunPruner, ret func() time.Duration) tasks.Definition {
	return PruneDefinition(p, ret, func() time.Time { return pruneNow })
}

func TestPrune_Conformance(t *testing.T) {
	taskstest.Conformance(t, pruneDef(&fakePruner{}, retention(time.Hour)))
}

// 05-tasks 5T.10: core.tasks.prune is scheduled from core.tasks.schedules.prune,
// writes the run store, and must be startable by core's system actor (so not
// an AuthRequired scope, SEC2-15).
func TestPrune_Declaration(t *testing.T) {
	d := pruneDef(&fakePruner{}, retention(time.Hour))
	if d.Kind() != KindPrune || KindPrune != "core.tasks.prune" {
		t.Errorf("kind = %q", d.Kind())
	}
	if d.Shape() != tasks.ShapeTask {
		t.Errorf("shape = %v", d.Shape())
	}
	c := tasks.CommonOf(d)
	if !slices.Equal(c.Effects.WriteSet(), []entity.Ref{tasks.Res("core", "tasks")}) || c.Effects.IsReadOnly() {
		t.Errorf("writes = %v, want one write on core/tasks", c.Effects.WriteSet())
	}
	if c.Schedule == nil || c.Schedule.ConfigKey() != "core.tasks.schedules.prune" || PruneScheduleKey != "core.tasks.schedules.prune" {
		t.Fatalf("Schedule = %+v", c.Schedule)
	}
	if !c.Schedule.IsLive() || c.Schedule.IsPreview() {
		t.Errorf("Schedule live=%v preview=%v, want live", c.Schedule.IsLive(), c.Schedule.IsPreview())
	}
	if got := c.Perms.Start.Permissions(); !slices.Equal(got, []rbac.Permission{"core:tasks.manage"}) {
		t.Errorf("Perms.Start = %v, want core:tasks.manage", got)
	}
	if c.Resume != tasks.ResumeFromZero || c.Priority != tasks.PriorityLow {
		t.Errorf("Resume %v Priority %v", c.Resume, c.Priority)
	}
}

func TestPrune_NilPrunerFailsClosed(t *testing.T) {
	if len(tasks.Problems(PruneDefinition(nil, retention(time.Hour), nil))) == 0 {
		t.Error("no problem reported for a nil pruner")
	}
	if len(tasks.Problems(PruneDefinition(&fakePruner{}, nil, nil))) == 0 {
		t.Error("no problem reported for a nil retention")
	}
}

// The cutoff is now minus the retention read at the start of the run.
func TestPrune_CutoffIsNowMinusRetention(t *testing.T) {
	f := &fakePruner{left: 3}
	if _, err := runPrune(t, pruneDef(f, retention(90*24*time.Hour)), taskstest.NewFakeRun(t.Context(), KindPrune)); err != nil {
		t.Fatal(err)
	}
	calls := f.snapshot()
	if len(calls) == 0 {
		t.Fatal("Prune never called")
	}
	want := pruneNow.Add(-90 * 24 * time.Hour)
	for _, c := range calls {
		if !c.before.Equal(want) {
			t.Errorf("before = %v, want %v", c.before, want)
		}
	}
}

// A retention of zero or less keeps everything: Prune is not called.
func TestPrune_NonPositiveRetentionKeepsEverything(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Hour} {
		f := &fakePruner{left: 5}
		res, err := runPrune(t, pruneDef(f, retention(d)), taskstest.NewFakeRun(t.Context(), KindPrune))
		if err != nil {
			t.Fatal(err)
		}
		if n := len(f.snapshot()); n != 0 {
			t.Errorf("retention %v: Prune called %d times", d, n)
		}
		if res.Deleted != 0 || !res.Kept {
			t.Errorf("retention %v: result %+v, want Kept and nothing deleted", d, res)
		}
	}
}

// An enormous retention must keep everything, never wrap into a cutoff at or
// after now (which would prune every terminal run).
func TestPrune_HugeRetentionNeverYieldsAFutureCutoff(t *testing.T) {
	for _, d := range []time.Duration{time.Duration(1<<63 - 1), 1 << 62, 100 * 365 * 24 * time.Hour} {
		f := &fakePruner{}
		if _, err := runPrune(t, pruneDef(f, retention(d)), taskstest.NewFakeRun(t.Context(), KindPrune)); err != nil {
			t.Fatal(err)
		}
		for _, c := range f.snapshot() {
			if !c.before.Before(pruneNow) {
				t.Errorf("retention %v: cutoff %v is not before now %v", d, c.before, pruneNow)
			}
		}
	}
}

// The task drains: it calls Prune until a call comes back short.
func TestPrune_DrainsInChunks(t *testing.T) {
	f := &fakePruner{left: 2*pruneChunk + 7}
	res, err := runPrune(t, pruneDef(f, retention(time.Hour)), taskstest.NewFakeRun(t.Context(), KindPrune))
	if err != nil {
		t.Fatal(err)
	}
	if res.Deleted != int64(2*pruneChunk+7) {
		t.Errorf("deleted = %d, want %d", res.Deleted, 2*pruneChunk+7)
	}
	calls := f.snapshot()
	if len(calls) != 3 {
		t.Fatalf("Prune called %d times, want 3", len(calls))
	}
	for _, c := range calls {
		if c.limit != pruneChunk {
			t.Errorf("limit = %d, want %d", c.limit, pruneChunk)
		}
	}
}

// A count that is an exact multiple of the chunk costs one extra empty call:
// the loop stops on a short call, never on a guess.
func TestPrune_ExactMultipleEndsOnAnEmptyCall(t *testing.T) {
	f := &fakePruner{left: pruneChunk}
	res, err := runPrune(t, pruneDef(f, retention(time.Hour)), taskstest.NewFakeRun(t.Context(), KindPrune))
	if err != nil {
		t.Fatal(err)
	}
	if res.Deleted != pruneChunk || len(f.snapshot()) != 2 {
		t.Errorf("deleted %d in %d calls, want %d in 2", res.Deleted, len(f.snapshot()), pruneChunk)
	}
}

func TestPrune_NothingToDo(t *testing.T) {
	f := &fakePruner{}
	res, err := runPrune(t, pruneDef(f, retention(time.Hour)), taskstest.NewFakeRun(t.Context(), KindPrune))
	if err != nil || res.Deleted != 0 || res.Kept {
		t.Fatalf("res %+v err %v", res, err)
	}
	if n := len(f.snapshot()); n != 1 {
		t.Errorf("Prune called %d times, want 1", n)
	}
}

// An error stops the pass and surfaces; what an earlier chunk deleted is
// reported in the status line (the executor stores no result for a failed run).
func TestPrune_ErrorStopsAndSurfaces(t *testing.T) {
	boom := errors.New("done index unreadable")
	f := &fakePruner{left: 10 * pruneChunk, failAfter: 2, err: boom}
	h := taskstest.NewFakeRun(t.Context(), KindPrune)
	_, err := runPrune(t, pruneDef(f, retention(time.Hour)), h)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap %v", err, boom)
	}
	if n := len(f.snapshot()); n != 3 {
		t.Errorf("Prune called %d times, want 3 (two good chunks, then the failure)", n)
	}
}

// A canceled run stops before taking another chunk.
func TestPrune_CancelStopsBetweenChunks(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	f := &fakePruner{left: 100 * pruneChunk, onCall: func(n int) {
		if n == 2 {
			cancel()
		}
	}}
	_, err := runPrune(t, pruneDef(f, retention(time.Hour)), taskstest.NewFakeRun(ctx, KindPrune))
	if err == nil {
		t.Fatal("a canceled run returned no error")
	}
	if n := len(f.snapshot()); n > 3 {
		t.Errorf("Prune called %d times after a cancel at call 2", n)
	}
}

// End to end over the native run store: old terminal runs go, a run that is
// not terminal stays, and a second pass finds nothing.
func TestTasksPrune_TerminalOnlyByRetention(t *testing.T) {
	release := make(chan struct{})
	started := make(chan tasks.RunID, 1)
	var h *harness
	defs := []tasks.Definition{
		taskDef("gotham.patrol", okBody),
		taskDef("gotham.stakeout", func(rc *tasks.RC, _ noParams) (string, error) {
			started <- rc.RunID()
			<-release
			return "", nil
		}),
	}
	// The prune kind is built over the harness's own run store below; the
	// harness needs its definition at boot, so it reads the store through a
	// forwarding pruner that is pointed at it afterwards.
	fwd := &forwardPruner{}
	defs = append(defs, PruneDefinition(fwd, retention(24*time.Hour), func() time.Time { return time.Now().Add(72 * time.Hour) }))
	h = newHarness(t, defs, nil)
	fwd.set(h.stores.Runs)
	t.Cleanup(func() { close(release) })

	var old []tasks.RunID
	for range 5 {
		id := h.start("gotham.patrol", tasks.StartRequest{})
		h.waitState(id, tasks.StateSucceeded)
		old = append(old, id)
	}
	// A run imported from HEAD (a terminal row the cutover staged through the
	// native importer) ages out by the same rule as a native one.
	imp := tasks.RunRecord{
		ID: "01HEADOPDONE0000000000000A", Kind: "gotham.patrol", Mode: tasks.ModeLive, State: tasks.StateSucceeded,
		Actor: tasks.Actor{UserID: "bruce", AuthMethod: "session"}, Trigger: tasks.TriggerManual,
		Created: time.Now().Add(-3 * time.Hour), Started: time.Now().Add(-2 * time.Hour), Finished: time.Now().Add(-time.Hour),
		Priority: tasks.PriorityNormal, Params: json.RawMessage(`{}`), Attempt: 1, FenceEpoch: 1,
	}
	b := h.ks.NewIndexedBatch()
	if err := tasks.NativeImporter(h.ks).Run(t.Context(), b, imp); err != nil {
		t.Fatalf("import: %v", err)
	}
	if err := b.Commit(t.Context(), storage.Sync); err != nil {
		t.Fatalf("commit import: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	old = append(old, imp.ID)
	live := h.start("gotham.stakeout", tasks.StartRequest{})
	<-started

	pid := h.start(string(KindPrune), tasks.StartRequest{})
	r := h.waitState(pid, tasks.StateSucceeded)
	var res PruneResult
	if err := json.Unmarshal(r.Result, &res); err != nil {
		t.Fatalf("result %s: %v", r.Result, err)
	}
	if res.Deleted != 6 {
		t.Errorf("deleted = %d, want the 5 patrol runs and the imported one (the stakeout is running and the prune run itself is not terminal yet)", res.Deleted)
	}
	for _, id := range old {
		if _, err := h.stores.Runs.Get(t.Context(), id); !errors.Is(err, tasks.ErrRunNotFound) {
			t.Errorf("run %s: Get err = %v, want ErrRunNotFound", id, err)
		}
	}
	if got := h.get(live); got.State.Terminal() {
		t.Errorf("the running stakeout was touched: state %s", got.State)
	}

	r2 := h.waitState(h.start(string(KindPrune), tasks.StartRequest{}), tasks.StateSucceeded)
	var res2 PruneResult
	if err := json.Unmarshal(r2.Result, &res2); err != nil {
		t.Fatal(err)
	}
	// The first prune run is itself an old terminal run by the fake clock.
	if res2.Deleted != 1 {
		t.Errorf("second pass deleted %d, want 1 (the first pass's own run)", res2.Deleted)
	}
}

// forwardPruner lets a definition be built before the store it prunes exists.
type forwardPruner struct {
	mu sync.Mutex
	p  RunPruner
}

func (f *forwardPruner) set(p RunPruner) { f.mu.Lock(); f.p = p; f.mu.Unlock() }

func (f *forwardPruner) Prune(ctx context.Context, before time.Time, limit int) (int, error) {
	f.mu.Lock()
	p := f.p
	f.mu.Unlock()
	if p == nil {
		return 0, errors.New("forwardPruner: no store")
	}
	return p.Prune(ctx, before, limit)
}
