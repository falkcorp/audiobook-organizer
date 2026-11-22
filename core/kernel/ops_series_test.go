// file: core/kernel/ops_series_test.go
// version: 1.0.0
// guid: d3a7e591-4f08-4c26-8b1e-7a5c02f9d614
// last-edited: 2026-11-22

package kernel

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/internal/executor"
	"github.com/falkcorp/mediacore/metrics/metricstest"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// opsUncovered are the instruments this scenario cannot reach from a kernel
// boot, each covered where its trigger lives: a run that yields in place and
// a stored record that cannot be decoded. Every other name in
// executor.InstrumentNames is in testdata/ops_series.golden, so an instrument
// added without a sample here, or without being listed here, fails.
var opsUncovered = []string{"yields", "undecodable_records"}

// guardKind is a writer that holds its resource until the test lets it go.
const guardKind tasks.Kind = "gotham.guard"

// countKind finishes at once having locked a ref and counted three items.
const countKind tasks.Kind = "gotham.count"

type guardParams struct {
	Round int `json:"round"`
}

// TestOpsSeries_Golden (05-metrics §5 item 5, §12): the ops scope the kernel
// builds with the executor serves exactly the families of
// testdata/ops_series.golden, each with its type and label set. A family with
// no sample exports nothing, so the test completes one run, then holds a
// writer in flight with a second queued behind its write set, seeds a held
// run of a disabled module and records the three scheduler families through
// the executor's ScheduleMetrics before it gathers.
func TestOpsSeries_Golden(t *testing.T) {
	release := make(chan struct{})
	var once = func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}
	t.Cleanup(once)
	started := make(chan struct{}, 2)

	g, _ := patroller(nil, func(r *module.Registrar) {
		r.Resources("cave")
		r.Lanes(tasks.Lane{ID: "gotham.signal", Title: "Signal"})
		r.Tasks(tasks.Task(countKind, tasks.TaskSpec[struct{}, struct{}]{
			Title:   "Count the cells",
			Effects: tasks.ReadOnly(),
			Perms:   tasks.Permissions{Start: rbac.Need("gotham:patrol")},
			Resume:  tasks.ResumeFromZero,
			Run: func(rc *tasks.RC, _ struct{}) (struct{}, error) {
				unlock, err := rc.LockRefs(tasks.Res("gotham", "cave"))
				if err != nil {
					return struct{}{}, err
				}
				defer unlock()
				rc.Progress().Set(3, tasks.Known(3))
				return struct{}{}, nil
			},
		}))
		r.Tasks(tasks.Task(guardKind, tasks.TaskSpec[guardParams, struct{}]{
			Title:   "Guard the cave",
			Effects: tasks.Writes(tasks.Res("gotham", "cave")),
			Perms:   tasks.Permissions{Start: rbac.Need("gotham:patrol")},
			Resume:  tasks.ResumeFromZero,
			Run: func(rc *tasks.RC, _ guardParams) (struct{}, error) {
				rc.Progress().Set(3, tasks.Known(10))
				started <- struct{}{}
				select {
				case <-release:
				case <-rc.Context().Done():
				}
				return struct{}{}, nil
			},
		}))
	})
	g = g.with(func(m *module.Manifest) { m.TaskLedger = append(m.TaskLedger, guardKind, countKind) })
	c, _ := taskConfig(t, g, arkham())
	ctx := bootseam.With(t.Context(), &bootseam.Seams{TaskRuns: []bootseam.TaskRunSeed{{Kind: string(cleanupKind), N: 1}}})
	k, err := newKCtx(t, ctx, c)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { once(); _ = k.closeCore(t.Context(), time.Now().Add(defaultShutdownBudget)) })
	if err := k.StartTasks(t.Context()); err != nil {
		t.Fatalf("StartTasks: %v", err)
	}
	cl := k.TaskClient()
	start := func(kind tasks.Kind, params any) tasks.RunRef {
		t.Helper()
		raw, err := json.Marshal(params)
		if err != nil {
			t.Fatal(err)
		}
		ref, err := cl.Start(asAdmin(t.Context()), kind, tasks.StartRequest{Mode: tasks.ModeRequestLive, Params: raw})
		if err != nil {
			t.Fatalf("Start %s: %v", kind, err)
		}
		return ref
	}

	if v := waitRun(t, cl, start(patrolKind, nil).ID); v.State != tasks.StateSucceeded {
		t.Fatalf("patrol ended %s (%s)", v.State, v.Reason)
	}
	if v := waitRun(t, cl, start(countKind, struct{}{}).ID); v.State != tasks.StateSucceeded {
		t.Fatalf("count ended %s (%s)", v.State, v.Reason)
	}
	first, second := start(guardKind, guardParams{Round: 1}), start(guardKind, guardParams{Round: 2})
	select {
	case <-started:
	case <-time.After(30 * time.Second):
		t.Fatal("the first guard never started")
	}
	waitState(t, cl, first.ID, tasks.StateRunning)
	waitState(t, cl, second.ID, tasks.StateQueued)

	sm := executor.Schedules(k.tasks.exec)
	sm.ScheduleLag(patrolKind, time.Second)
	sm.ScheduleMissed(patrolKind, 2)
	sm.ScheduleError(patrolKind)

	metricstest.AssertSeries(t, k.provider, k.tasks.ops, "testdata/ops_series.golden")

	// Every instrument is sampled in the golden or named in opsUncovered.
	want, err := metricstest.ReadGolden("testdata/ops_series.golden")
	if err != nil {
		t.Fatal(err)
	}
	prefix := metricstest.FamilyPrefix(k.tasks.ops)
	for _, n := range executor.InstrumentNames() {
		norm := strings.ReplaceAll(n, ".", "_")
		inGolden := slices.ContainsFunc(want, func(s metricstest.Series) bool {
			f := strings.TrimPrefix(s.Family, prefix)
			return f == norm || strings.HasPrefix(f, norm+"_")
		})
		if inGolden == slices.Contains(opsUncovered, n) {
			t.Errorf("instrument %q: in golden = %v, listed uncovered = %v (exactly one must hold)", n, inGolden, slices.Contains(opsUncovered, n))
		}
	}
	once()
	waitRun(t, cl, first.ID)
	waitRun(t, cl, second.ID)
}

// waitState waits until run id is in state want.
func waitState(t *testing.T, c tasks.Client, id tasks.RunID, want tasks.RunState) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		v, err := c.Get(asAdmin(t.Context()), id)
		if err != nil {
			t.Fatalf("Get %s: %v", id, err)
		}
		if v.State == want {
			return
		}
		if time.Now().After(deadline) || v.State.Terminal() {
			t.Fatalf("run %s is %s, want %s", id, v.State, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
