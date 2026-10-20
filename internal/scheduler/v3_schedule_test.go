// file: internal/scheduler/v3_schedule_test.go
// version: 1.2.2
// guid: 5a90d3e7-2c18-4b64-8e0f-71b6c4a9d235
// last-edited: 2026-10-20

package scheduler

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

type fakeSchedStore struct {
	mu   sync.Mutex
	recs map[string]database.SchedState
	// onPut, when set, sees each record just before it is stored.
	onPut func(database.SchedState)
}

func (f *fakeSchedStore) GetSchedV3(id string) (*database.SchedState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.recs[id]; ok {
		return &r, nil
	}
	return nil, nil
}

func (f *fakeSchedStore) PutSchedV3(st database.SchedState) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.onPut != nil {
		f.onPut(st)
	}
	if f.recs == nil {
		f.recs = map[string]database.SchedState{}
	}
	f.recs[st.DefID] = st
	return nil
}

type enqCall struct {
	defID  string
	params map[string]any
}

type v3Rig struct {
	ts     *TaskScheduler
	now    time.Time
	store  *fakeSchedStore
	calls  []enqCall
	active map[string]bool
	v2Cron map[string]string
}

func newV3Rig(t *testing.T) *v3Rig {
	t.Helper()
	r := &v3Rig{ts: NewTaskScheduler(reachabilityTestDeps()), now: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC),
		store: &fakeSchedStore{}, active: map[string]bool{}, v2Cron: map[string]string{}}
	r.ts.v3.now = func() time.Time { return r.now }
	r.ts.v3.loc = time.UTC
	r.ts.v3.store = func() schedStore { return r.store }
	r.ts.v3.hasActive = func(id string) bool { return r.active[id] }
	r.ts.v3.v2Cron = func(id string) (string, bool) { c, ok := r.v2Cron[id]; return c, ok }
	r.ts.v3.enqueue = func(_ context.Context, id string, params json.RawMessage) (string, error) {
		var m map[string]any
		if err := json.Unmarshal(params, &m); err != nil {
			t.Fatalf("params for %s are not an object: %v", id, err)
		}
		r.calls = append(r.calls, enqCall{id, m})
		return "op-" + id, nil
	}
	return r
}

func (r *v3Rig) poll(at time.Time) { r.now = at; r.ts.pollV3(at) }

func (r *v3Rig) register(t *testing.T, defs ...ops.Definition) {
	t.Helper()
	if err := r.ts.RegisterSchedules(defs); err != nil {
		t.Fatalf("RegisterSchedules: %v", err)
	}
}

func schedDef(id string, e ops.Effects, s *ops.Schedule) ops.Definition {
	return ops.Task(id, ops.TaskSpec[ops.NoParams, ops.NoResult]{
		Title:      id,
		Help:       "synthetic",
		Effects:    e,
		Permission: auth.PermSettingsManage,
		Schedule:   s,
		Run:        func(*ops.RC, ops.NoParams) (ops.NoResult, error) { return ops.NoResult{}, nil }})
}

func readDef(id string, s *ops.Schedule) ops.Definition {
	return schedDef(id, ops.ReadOnly(ops.ResBooks), s)
}

func (r *v3Rig) status(t *testing.T, id string) ScheduleStatus {
	t.Helper()
	for _, st := range r.ts.ScheduleStatuses() {
		if st.DefID == id {
			return st
		}
	}
	t.Fatalf("no status for %s", id)
	return ScheduleStatus{}
}

func TestEveryUsesDurableClock(t *testing.T) {
	r := newV3Rig(t)
	r.register(t, readDef("synthetic.every", ops.Every(time.Hour)))
	t0 := r.now
	r.poll(t0) // first observation seeds and declines, as the interval clock does
	if len(r.calls) != 0 {
		t.Fatalf("fired on first observation: %v", r.calls)
	}
	if got, ok := r.ts.loadIntervalLastTick("synthetic.every"); !ok || !got.Equal(t0) {
		t.Fatalf("durable clock under the def id = %v,%v want %v", got, ok, t0)
	}
	if next, ok := r.ts.NextFire("synthetic.every"); !ok || !next.Equal(t0.Add(time.Hour)) {
		t.Errorf("NextFire = %v,%v want last+interval %v", next, ok, t0.Add(time.Hour))
	}
	r.poll(t0.Add(59 * time.Minute))
	if len(r.calls) != 0 {
		t.Fatal("fired before the interval elapsed")
	}
	r.poll(t0.Add(61 * time.Minute))
	if len(r.calls) != 1 || r.calls[0].defID != "synthetic.every" {
		t.Fatalf("calls = %v, want one run", r.calls)
	}
	if got, _ := r.ts.loadIntervalLastTick("synthetic.every"); !got.Equal(t0.Add(61 * time.Minute)) {
		t.Errorf("clock not stamped at the fire: %v", got)
	}
	if rec, _ := r.store.GetSchedV3("synthetic.every"); rec == nil || rec.LastRunID != "op-synthetic.every" || rec.LastFire == nil {
		t.Errorf("schedule record after fire = %+v", rec)
	}
}

func TestDailyAtUsesDailyClock(t *testing.T) {
	r := newV3Rig(t)
	r.register(t, readDef("synthetic.daily", ops.DailyAt("03:00")))
	r.poll(r.now) // seed at 2026-10-16 12:00
	r.poll(time.Date(2026, 10, 17, 2, 59, 0, 0, time.UTC))
	if len(r.calls) != 0 {
		t.Fatal("fired before 03:00")
	}
	fire := time.Date(2026, 10, 17, 3, 0, 0, 0, time.UTC)
	r.poll(fire)
	if len(r.calls) != 1 {
		t.Fatalf("calls = %v, want one run at 03:00", r.calls)
	}
	want := nextDailyAt(fire, 3, 0, time.UTC)
	if next, ok := r.ts.NextFire("synthetic.daily"); !ok || !next.Equal(want) {
		t.Errorf("NextFire = %v,%v want what the daily clock computes, %v", next, ok, want)
	}
}

func TestCronFires(t *testing.T) {
	r := newV3Rig(t)
	r.register(t, readDef("synthetic.cron", ops.Cron("30 2 * * *")))
	r.poll(r.now)
	r.poll(time.Date(2026, 10, 17, 2, 29, 0, 0, time.UTC))
	if len(r.calls) != 0 {
		t.Fatal("fired before 02:30")
	}
	r.poll(time.Date(2026, 10, 17, 2, 30, 0, 0, time.UTC))
	if len(r.calls) != 1 {
		t.Fatalf("calls = %v, want one run", r.calls)
	}
	spec, _ := ParseCron("30 2 * * *")
	want, _ := spec.Next(time.Date(2026, 10, 17, 2, 30, 0, 0, time.UTC), time.UTC)
	if next, ok := r.ts.NextFire("synthetic.cron"); !ok || !next.Equal(want) {
		t.Errorf("NextFire = %v,%v want %v", next, ok, want)
	}
	r.poll(time.Date(2026, 10, 17, 2, 31, 0, 0, time.UTC))
	if len(r.calls) != 1 {
		t.Error("fired twice for one due time")
	}
}

func TestWindowOrderHonored(t *testing.T) {
	r := newV3Rig(t)
	before := slices.Clone(r.ts.maintenanceOrder)
	r.register(t,
		readDef("synthetic.b", ops.InMaintenanceWindow(20)),
		readDef("synthetic.a", ops.InMaintenanceWindow(20)),
		readDef("synthetic.first", ops.InMaintenanceWindow(5)))
	got := r.ts.maintenanceOrder
	if got[len(got)-1] != "library_scan" {
		t.Fatalf("library_scan is no longer last: %v", got[len(got)-4:])
	}
	// The v3 block sits immediately before library_scan, in (order, id) order,
	// and every pre-existing task keeps its place relative to the others.
	end := len(got) - 1
	if blk := got[end-3 : end]; !slices.Equal(blk, []string{"synthetic.first", "synthetic.a", "synthetic.b"}) {
		t.Fatalf("v3 window block = %v, want order then def id, just before library_scan", blk)
	}
	if rest := slices.Concat(got[:end-3], got[end:]); !slices.Equal(rest, before) {
		t.Fatal("existing tasks' window order changed")
	}
	// Without a library_scan entry the block is appended.
	r2 := newV3Rig(t)
	r2.ts.maintenanceOrder = slices.DeleteFunc(r2.ts.maintenanceOrder, func(n string) bool { return n == "library_scan" })
	r2.register(t, readDef("synthetic.only", ops.InMaintenanceWindow(1)))
	if o := r2.ts.maintenanceOrder; o[len(o)-1] != "synthetic.only" {
		t.Errorf("block not appended when library_scan is absent: %v", o[len(o)-2:])
	}
	for _, id := range []string{"synthetic.first", "synthetic.a", "synthetic.b"} {
		if !r.ts.reachableViaMaintenanceWindow(id) {
			t.Errorf("%s is not reachable via the maintenance window", id)
		}
	}
	op, err := r.ts.RunTaskWithSource("synthetic.a", "scheduled")
	if err != nil || op == nil || len(r.calls) != 1 || r.calls[0].defID != "synthetic.a" {
		t.Fatalf("window run: op=%v err=%v calls=%v", op, err, r.calls)
	}
	if _, ok := r.ts.NextFire("synthetic.a"); ok {
		t.Error("a window def reports a timer next-fire")
	}
}

func TestDisabledScheduleNeverFires(t *testing.T) {
	r := newV3Rig(t)
	r.v2Cron["synthetic.undriven"] = "0 1 * * *"
	r.v2Cron["maintenance.file-integrity-check"] = "30 2 * * *" // driven by a task (04 P1)
	r.register(t,
		opsregistry.FromV2(opsregistry.OperationDef{ID: "synthetic.undriven", DisplayName: "u"}),
		opsregistry.FromV2(opsregistry.OperationDef{ID: "maintenance.file-integrity-check", DisplayName: "f"}),
		opsregistry.FromV2(opsregistry.OperationDef{ID: "synthetic.manual", DisplayName: "m"}))
	for range 5 {
		r.poll(r.now.Add(24 * time.Hour))
	}
	if len(r.calls) != 0 {
		t.Fatalf("an adapted def fired from the v3 scheduler: %v", r.calls)
	}
	if st := r.status(t, "synthetic.undriven"); st.Kind != "disabled" || st.Active {
		t.Errorf("undriven status = %+v, want disabled and inactive", st)
	}
	if st := r.status(t, "maintenance.file-integrity-check"); st.Kind != "cron" || !st.Active {
		t.Errorf("driven status = %+v, want an active cron the task drives", st)
	}
	if _, ok := r.ts.NextFire("synthetic.undriven"); ok {
		t.Error("a disabled schedule reports a next fire")
	}
	for _, st := range r.ts.ScheduleStatuses() {
		if st.DefID == "synthetic.manual" {
			t.Error("a def with no schedule is listed")
		}
	}
}

func TestCoalescesIntoRunning(t *testing.T) {
	r := newV3Rig(t)
	r.register(t, readDef("synthetic.busy", ops.Every(time.Hour)))
	t0 := r.now
	r.poll(t0)
	r.active["synthetic.busy"] = true
	r.poll(t0.Add(2 * time.Hour))
	if len(r.calls) != 0 {
		t.Fatalf("enqueued while the previous run was active: %v", r.calls)
	}
	r.active["synthetic.busy"] = false
	r.poll(t0.Add(2*time.Hour + 30*time.Minute))
	if len(r.calls) != 0 {
		t.Fatal("re-fired within the interval after a coalesced due check")
	}
	r.poll(t0.Add(3*time.Hour + 31*time.Minute))
	if len(r.calls) != 1 {
		t.Fatalf("calls = %v, want the next interval to run", r.calls)
	}
}

func TestMissedFiresCountedOneCatchUp(t *testing.T) {
	r := newV3Rig(t)
	r.register(t, readDef("synthetic.missed", ops.Every(time.Hour)))
	t0 := r.now
	r.poll(t0)
	r.poll(t0.Add(5*time.Hour + 10*time.Minute)) // due at +1h, so +1h..+5h were all due
	if len(r.calls) != 1 {
		t.Fatalf("calls = %d, want exactly one catch-up run", len(r.calls))
	}
	if st := r.status(t, "synthetic.missed"); st.Missed != 4 {
		t.Errorf("Missed = %d, want 4", st.Missed)
	}
	if rec, _ := r.store.GetSchedV3("synthetic.missed"); rec == nil || rec.Missed != 4 {
		t.Errorf("persisted record = %+v, want Missed 4", rec)
	}
	// Cron: three nightly fires missed.
	r2 := newV3Rig(t)
	r2.register(t, readDef("synthetic.cronmissed", ops.Cron("0 3 * * *")))
	r2.poll(r2.now)
	r2.poll(time.Date(2026, 10, 19, 4, 0, 0, 0, time.UTC)) // 17th, 18th and 19th 03:00 were due
	if len(r2.calls) != 1 || r2.status(t, "synthetic.cronmissed").Missed != 2 {
		t.Errorf("cron catch-up: calls=%d missed=%d, want 1 and 2", len(r2.calls), r2.status(t, "synthetic.cronmissed").Missed)
	}
}

func TestScheduledWriterWithoutLiveRunsPreview(t *testing.T) {
	r := newV3Rig(t)
	r.register(t,
		schedDef("synthetic.preview", ops.Writes(ops.ResBooks), ops.Every(time.Hour).Preview()),
		// ValidateCatalog refuses a writer with neither; if one slipped through,
		// the scheduler still must not default to Live.
		schedDef("synthetic.nomode", ops.Writes(ops.ResBooks), ops.Every(time.Hour)))
	t0 := r.now
	r.poll(t0)
	r.poll(t0.Add(time.Hour))
	if len(r.calls) != 2 {
		t.Fatalf("calls = %v", r.calls)
	}
	for _, c := range r.calls {
		if c.params["dry_run"] != true {
			t.Errorf("%s ran with params %v, want dry_run=true", c.defID, c.params)
		}
	}
}

func TestScheduledWriterWithLiveRunsLive(t *testing.T) {
	r := newV3Rig(t)
	r.register(t,
		schedDef("synthetic.live", ops.Writes(ops.ResBooks), ops.Every(time.Hour).Live().Params(map[string]any{"limit": 5, "dry_run": true})),
		readDef("synthetic.reader", ops.Every(time.Hour)))
	t0 := r.now
	r.poll(t0)
	r.poll(t0.Add(time.Hour))
	byID := map[string]map[string]any{}
	for _, c := range r.calls {
		byID[c.defID] = c.params
	}
	if p := byID["synthetic.live"]; p["dry_run"] != false || p["limit"] != float64(5) {
		t.Errorf("live writer params = %v, want dry_run=false and limit kept", p)
	}
	if _, has := byID["synthetic.reader"]["dry_run"]; has {
		t.Errorf("a read-only def was handed dry_run: %v", byID["synthetic.reader"])
	}
}

func TestWhenGateReReadEachTick(t *testing.T) {
	r := newV3Rig(t)
	gate := false
	r.register(t, readDef("synthetic.gated", ops.Every(time.Hour).When(func() bool { return gate })))
	t0 := r.now
	r.poll(t0) // gate off: nothing is seeded or fired
	r.poll(t0.Add(3 * time.Hour))
	if len(r.calls) != 0 {
		t.Fatal("fired with the gate off")
	}
	if st := r.status(t, "synthetic.gated"); st.Active {
		t.Error("gate off, but status is Active")
	}
	if _, ok := r.ts.NextFire("synthetic.gated"); ok {
		t.Error("gate off, but NextFire reports a time")
	}
	gate = true
	r.poll(t0.Add(4 * time.Hour)) // first observation with the gate on seeds the clock
	r.poll(t0.Add(5*time.Hour + time.Minute))
	if len(r.calls) != 1 {
		t.Fatalf("calls = %v, want one run once the gate opened and the interval elapsed", r.calls)
	}
	if rec, _ := r.store.GetSchedV3("synthetic.gated"); rec == nil {
		t.Error("no schedule record written after a gate edge and a fire")
	}
}

func TestSeedFromLegacyClock(t *testing.T) {
	r := newV3Rig(t)
	// library_size_refresh ran 10 minutes ago under its old task-name key.
	ran := r.now.Add(-10 * time.Minute)
	r.ts.stampIntervalTick("library_size_refresh", ran)
	delete(r.ts.tasks, "library_size_refresh") // the port PR removes the TaskDefinition
	r.register(t, readDef("library.size-refresh", ops.Every(6*time.Hour)))
	if got, ok := r.ts.loadIntervalLastTick("library.size-refresh"); !ok || !got.Equal(ran) {
		t.Fatalf("clock seeded to %v,%v want the legacy stamp %v", got, ok, ran)
	}
	r.poll(r.now)
	if len(r.calls) != 0 {
		t.Fatal("fired at once after the port although the old clock said it ran 10 minutes ago")
	}
	if next, _ := r.ts.NextFire("library.size-refresh"); !next.Equal(ran.Add(6 * time.Hour)) {
		t.Errorf("NextFire = %v, want legacy stamp + 6h", next)
	}
	// With a schedule record already stored, the legacy clock is not consulted.
	r2 := newV3Rig(t)
	r2.ts.stampIntervalTick("library_size_refresh", ran)
	delete(r2.ts.tasks, "library_size_refresh")
	_ = r2.store.PutSchedV3(database.SchedState{DefID: "library.size-refresh"})
	r2.register(t, readDef("library.size-refresh", ops.Every(6*time.Hour)))
	if _, ok := r2.ts.loadIntervalLastTick("library.size-refresh"); ok {
		t.Error("seeded from the legacy clock although a schedule record exists")
	}
}

func TestOnStartFiresOnce(t *testing.T) {
	r := newV3Rig(t)
	r.register(t, readDef("synthetic.start", ops.Every(time.Hour).OnStart()))
	r.ts.runV3OnStart()
	r.poll(r.now)
	r.poll(r.now.Add(time.Minute))
	if len(r.calls) != 1 {
		t.Fatalf("calls = %d, want the start run only", len(r.calls))
	}
}

func TestNativeDefDrivenByLegacyTaskIsNotFiredTwice(t *testing.T) {
	r := newV3Rig(t)
	// maintenance.file-integrity-check still has a TaskDefinition (04 P1).
	r.register(t, readDef("maintenance.file-integrity-check", ops.Cron("30 2 * * *")))
	r.poll(r.now)
	r.poll(r.now.Add(48 * time.Hour))
	if len(r.calls) != 0 {
		t.Fatalf("fired a def a legacy task still drives: %v", r.calls)
	}
	if st := r.status(t, "maintenance.file-integrity-check"); !st.Active {
		t.Errorf("status = %+v", st)
	}
}

func TestRegisterSchedulesReportsBadSchedule(t *testing.T) {
	r := newV3Rig(t)
	if err := r.ts.RegisterSchedules([]ops.Definition{readDef("synthetic.bad", ops.Cron("61 * * * *"))}); err == nil {
		t.Error("an invalid schedule registered without error")
	}
}

func TestLegacyTaskNamesCoverTaskV2DefIDs(t *testing.T) {
	for name, defID := range taskV2DefIDs {
		if name == "library_scan_full" {
			continue
		}
		if legacyTaskNames[defID] == "" {
			t.Errorf("task %s (def %s) has no legacyTaskNames entry", name, defID)
		}
	}
	if legacyTaskNameForDef("library.scan") != "library_scan" {
		t.Error("library.scan must map to library_scan")
	}
}

func TestGateClosedTimeIsNotMissed(t *testing.T) {
	gate := false
	t0 := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	week := t0.Add(7 * 24 * time.Hour)
	for name, tc := range map[string]struct {
		def  func() ops.Definition
		want int64 // missed after the gate reopens and the catch-up fires
	}{
		"every": {func() ops.Definition {
			return readDef("synthetic.g", ops.Every(time.Hour).When(func() bool { return gate }))
		}, 0},
		"daily_at": {func() ops.Definition {
			return readDef("synthetic.g", ops.DailyAt("03:00").When(func() bool { return gate }))
		}, 0},
		"cron": {func() ops.Definition {
			return readDef("synthetic.g", ops.Cron("0 * * * *").When(func() bool { return gate }))
		}, 0},
	} {
		t.Run(name, func(t *testing.T) {
			gate = true
			r := newV3Rig(t)
			r.register(t, tc.def())
			r.poll(t0) // seed
			gate = false
			for d := time.Duration(0); d <= 7*24*time.Hour; d += 6 * time.Hour {
				r.poll(t0.Add(d)) // a week with the gate closed
			}
			if len(r.calls) != 0 {
				t.Fatalf("fired with the gate closed: %v", r.calls)
			}
			gate = true
			r.poll(week.Add(time.Minute)) // the gate opens; the overdue run catches up once
			if len(r.calls) != 1 {
				t.Fatalf("calls = %d, want one catch-up run", len(r.calls))
			}
			if m := r.status(t, "synthetic.g").Missed; m != tc.want {
				t.Errorf("Missed = %d after a week with the gate closed, want %d", m, tc.want)
			}
		})
	}
}

func TestCountDueClipsToGateOpen(t *testing.T) {
	// Hourly due times; the gate opened at +3.5h and the poll is at +10h. Due
	// times before +3.5h are excused, those at +4h..+10h (7) count, one of them
	// being the catch-up run itself.
	first := time.Date(2026, 10, 16, 13, 0, 0, 0, time.UTC)
	step := func(t time.Time) (time.Time, bool) { return t.Truncate(time.Hour).Add(time.Hour), true }
	open := first.Add(3*time.Hour + 30*time.Minute)
	dueAt, missed := countDue(first, first.Add(10*time.Hour), open, step)
	if !dueAt.Equal(first.Add(4*time.Hour)) || missed != 6 {
		t.Errorf("dueAt=%v missed=%d, want +4h and 6", dueAt, missed)
	}
	// Nothing came due since the gate opened: one plain catch-up, nothing missed.
	dueAt, missed = countDue(first, open.Add(10*time.Minute), open, step)
	if !dueAt.Equal(open) || missed != 0 {
		t.Errorf("dueAt=%v missed=%d, want the open time and 0", dueAt, missed)
	}
}

func TestCronCatchUpCountIsCheap(t *testing.T) {
	// The catch-up count steps from one due time to the next: one Next call per
	// counted due time (plus the one that ends the walk), not a rescan from the
	// stamp each time. Counting calls is deterministic where a wall-clock bound
	// is not.
	spec, _ := ParseCron("* * * * *")
	first := time.Date(2026, 10, 16, 23, 0, 0, 0, time.UTC)
	now := first.Add(2000 * time.Minute)
	calls := 0
	dueAt, missed := countDue(first, now, time.Time{}, func(t time.Time) (time.Time, bool) {
		calls++
		return spec.Next(t, time.UTC)
	})
	if !dueAt.Equal(first) || missed != 999 {
		t.Fatalf("dueAt=%v missed=%d, want first and the 999 cap", dueAt, missed)
	}
	if calls != 999 {
		t.Errorf("countDue made %d Next calls, want exactly 999 (one per counted step)", calls)
	}
}

func TestCronNextScansOnlyFromTheAfterHour(t *testing.T) {
	// Next starts the first day one hour before `after`, not at midnight. A
	// candidate counter makes that observable without timing: from 23:30 an
	// every-minute expression examines the 91 minutes from 22:00, where a scan
	// from midnight would examine 1411.
	spec, _ := ParseCron("* * * * *")
	after := time.Date(2026, 10, 16, 23, 30, 0, 0, time.UTC)
	probes := 0
	got, ok := spec.next(after, time.UTC, &probes)
	if !ok || !got.Equal(after.Add(time.Minute)) {
		t.Fatalf("next = %v,%v want %v", got, ok, after.Add(time.Minute))
	}
	if probes > 100 {
		t.Errorf("next examined %d candidates, want at most 100 (scan must start near the after hour)", probes)
	}
}

func TestNextFireForLegacyDrivenDefs(t *testing.T) {
	r := newV3Rig(t)
	// maintenance.file-integrity-check is window-only today (interval 0): no timer.
	r.register(t,
		readDef("maintenance.file-integrity-check", ops.Cron("30 2 * * *")),
		readDef("library.size-refresh", ops.Every(6*time.Hour)))
	if _, ok := r.ts.NextFire("maintenance.file-integrity-check"); ok {
		t.Error("a window-only legacy task reports a timer next-fire")
	}
	// An interval task: last stamp + its own interval.
	task := r.ts.tasks["library_size_refresh"]
	task.IsEnabled = func() bool { return true }
	task.GetInterval = func() time.Duration { return 6 * time.Hour }
	last := r.now.Add(-time.Hour)
	r.ts.stampIntervalTick("library_size_refresh", last)
	want := last.Add(task.GetInterval())
	if got, ok := r.ts.NextFire("library.size-refresh"); !ok || !got.Equal(want) {
		t.Errorf("NextFire = %v,%v want the legacy task's next due %v", got, ok, want)
	}
	if st := r.status(t, "library.size-refresh"); st.NextFire == nil || !st.NextFire.Equal(want) {
		t.Errorf("status NextFire = %v, want %v", st.NextFire, want)
	}
	// A DailyAt legacy task uses the daily clock.
	r.ts.tasks["library_size_refresh"].DailyAt = "04:00"
	if got, ok := r.ts.NextFire("library.size-refresh"); !ok || !got.Equal(nextDailyAt(last, 4, 0, time.UTC)) {
		t.Errorf("daily legacy NextFire = %v,%v", got, ok)
	}
	// A disabled legacy task has no next fire.
	r.ts.tasks["library_size_refresh"].IsEnabled = func() bool { return false }
	if _, ok := r.ts.NextFire("library.size-refresh"); ok {
		t.Error("a disabled legacy task reports a next fire")
	}
}

func TestEveryEnqueueTargetInTasksIsInTaskV2DefIDs(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	entries, err := os.ReadDir(wd)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]*ast.File{}
	for _, de := range entries {
		n := de.Name()
		if de.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(wd, n), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files[n] = f
	}
	consts := map[string]string{}
	for _, f := range files {
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, sp := range gd.Specs {
				vs := sp.(*ast.ValueSpec)
				for i, n := range vs.Names {
					if i < len(vs.Values) {
						if bl, ok := vs.Values[i].(*ast.BasicLit); ok && bl.Kind == token.STRING {
							consts[n.Name], _ = strconv.Unquote(bl.Value)
						}
					}
				}
			}
		}
	}
	ids := map[string]bool{}
	for _, id := range taskV2DefIDs {
		ids[id] = true
	}
	found, named := 0, 0
	f := files["tasks.go"]
	if f == nil {
		t.Fatal("tasks.go not found next to the test")
	}
	// stack holds the composite literals around the node being visited, so an
	// EnqueueOp call can be tied to the TaskDefinition{Name: ...} it sits in.
	var stack []*ast.CompositeLit
	enclosingName := func() string {
		for _, s := range slices.Backward(stack) {
			for _, el := range s.Elts {
				kv, ok := el.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "Name" {
					if bl, ok := kv.Value.(*ast.BasicLit); ok && bl.Kind == token.STRING {
						name, _ := strconv.Unquote(bl.Value)
						return name
					}
				}
			}
		}
		return ""
	}
	var visit func(n ast.Node) bool
	visit = func(n ast.Node) bool {
		if n == nil {
			return false
		}
		if cl, ok := n.(*ast.CompositeLit); ok {
			stack = append(stack, cl)
			for _, el := range cl.Elts {
				ast.Inspect(el, visit)
			}
			stack = stack[:len(stack)-1]
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) < 2 {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); !ok || sel.Sel.Name != "EnqueueOp" {
			return true
		}
		var id string
		switch a := call.Args[1].(type) {
		case *ast.BasicLit:
			id, _ = strconv.Unquote(a.Value)
		case *ast.Ident:
			id = consts[a.Name]
		}
		pos := fset.Position(call.Pos())
		if id == "" {
			t.Errorf("%s: cannot resolve the def ID passed to EnqueueOp; use a string literal or a package const", pos)
			return true
		}
		found++
		if name := enclosingName(); name != "" {
			named++
			if taskV2DefIDs[name] != id {
				t.Errorf("%s: task %q enqueues %q but taskV2DefIDs[%q] = %q, so RegisterSchedules would also fire it from ops.Schedule (double run)", pos, name, id, name, taskV2DefIDs[name])
			}
		} else if !ids[id] {
			t.Errorf("%s: a task enqueues %q but taskV2DefIDs has no entry for it, so RegisterSchedules would also fire it from ops.Schedule (double run)", pos, id)
		}
		return true
	}
	ast.Inspect(f, visit)
	if found < 20 {
		t.Fatalf("found only %d EnqueueOp calls in tasks.go; the scan is not seeing the file", found)
	}
	if named < 10 {
		t.Fatalf("only %d of %d EnqueueOp calls resolved to an enclosing TaskDefinition name; the scan is not seeing the Name fields", named, found)
	}
}

// restart starts a second process: a fresh TaskScheduler that shares the sched
// store and the durable interval clock of the first.
func (r *v3Rig) restart(t *testing.T, at time.Time) *v3Rig {
	t.Helper()
	r2 := newV3Rig(t)
	r2.store = r.store
	r2.ts.intervalTick = maps.Clone(r.ts.intervalTick)
	r2.now = at
	return r2
}

func TestRestartAcrossClosedGateIsNotMissed(t *testing.T) {
	t0 := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		sched  func(gate func() bool) *ops.Schedule
		closed time.Duration
	}{
		{"every", func(g func() bool) *ops.Schedule { return ops.Every(time.Hour).When(g) }, 3 * 24 * time.Hour},
		{"daily_at", func(g func() bool) *ops.Schedule { return ops.DailyAt("03:00").When(g) }, 7 * 24 * time.Hour},
		{"cron", func(g func() bool) *ops.Schedule { return ops.Cron("0 * * * *").When(g) }, 7 * 24 * time.Hour},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/open at boot", func(t *testing.T) {
			gate := true
			mk := func() ops.Definition { return readDef("synthetic.g", tc.sched(func() bool { return gate })) }
			r := newV3Rig(t)
			r.now = t0
			r.register(t, mk())
			r.poll(t0)
			gate = false
			for d := time.Hour; d <= tc.closed; d += 6 * time.Hour {
				r.poll(t0.Add(d))
			}
			// Restart with the gate open at boot.
			gate = true
			r2 := r.restart(t, t0.Add(tc.closed+time.Minute))
			r2.register(t, mk())
			r2.poll(r2.now)
			if m := r2.status(t, "synthetic.g").Missed; m != 0 {
				t.Errorf("Missed = %d after a restart across a closed gate, want 0", m)
			}
			if len(r2.calls) != 1 {
				t.Errorf("calls = %d, want one catch-up run", len(r2.calls))
			}
		})
		t.Run(tc.name+"/closed at boot then open", func(t *testing.T) {
			gate := true
			mk := func() ops.Definition { return readDef("synthetic.g", tc.sched(func() bool { return gate })) }
			r := newV3Rig(t)
			r.register(t, mk())
			r.poll(t0)
			gate = false
			r.poll(t0.Add(time.Hour))
			r2 := r.restart(t, t0.Add(tc.closed))
			r2.register(t, mk()) // closed at boot
			r2.poll(r2.now)
			gate = true
			r2.poll(r2.now.Add(time.Minute))
			if m := r2.status(t, "synthetic.g").Missed; m != 0 {
				t.Errorf("Missed = %d, want 0", m)
			}
		})
		t.Run(tc.name+"/closed at first boot is recorded", func(t *testing.T) {
			gate := false
			mk := func() ops.Definition { return readDef("synthetic.g", tc.sched(func() bool { return gate })) }
			r := newV3Rig(t)
			r.register(t, mk())
			rec, _ := r.store.GetSchedV3("synthetic.g")
			if rec == nil || rec.GateClosedAt == nil {
				t.Fatalf("a gate closed at boot was not persisted: %+v", rec)
			}
			// Reopen: the open edge clears the record.
			gate = true
			r.poll(t0)
			rec, _ = r.store.GetSchedV3("synthetic.g")
			if rec == nil || rec.GateClosedAt != nil {
				t.Errorf("open edge did not clear GateClosedAt: %+v", rec)
			}
		})
	}
}

// The open edge clears the closed mark only after the claim has stamped the
// clock, so a crash between the two cannot count the closed period as missed.
func TestOpenEdgeClearsMarkAfterClockStamp(t *testing.T) {
	gate := false
	r := newV3Rig(t)
	r.register(t, readDef("synthetic.g", ops.Every(time.Hour).When(func() bool { return gate })))
	stamped, cleared := false, false
	r.store.onPut = func(st database.SchedState) {
		if st.GateClosedAt != nil || cleared {
			return // only the first write that clears the mark matters
		}
		cleared = true
		r.ts.mu.Lock()
		_, stamped = r.ts.intervalTick["synthetic.g"]
		r.ts.mu.Unlock()
	}
	gate = true
	r.poll(r.now.Add(time.Minute))
	if !cleared {
		t.Fatal("the open edge never cleared the closed mark")
	}
	if !stamped {
		t.Error("the closed mark was cleared before the claim stamped the clock")
	}
}

// A window definition is never polled, so a gate closed at boot must not leave
// a mark that nothing would clear.
func TestWindowDefClosedAtBootGetsNoClosedMark(t *testing.T) {
	r := newV3Rig(t)
	r.register(t, readDef("synthetic.w", ops.InMaintenanceWindow(20).When(func() bool { return false })))
	if rec, _ := r.store.GetSchedV3("synthetic.w"); rec != nil && rec.GateClosedAt != nil {
		t.Errorf("window def got GateClosedAt %v", rec.GateClosedAt)
	}
}

// A restart while the gate stayed OPEN is real downtime and still counts.
func TestRestartWithOpenGateStillCountsDowntime(t *testing.T) {
	t0 := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	mk := func() ops.Definition { return readDef("synthetic.g", ops.Every(time.Hour)) }
	r := newV3Rig(t)
	r.register(t, mk())
	r.poll(t0)
	r2 := r.restart(t, t0.Add(10*time.Hour+time.Minute))
	r2.register(t, mk())
	r2.poll(r2.now)
	if m := r2.status(t, "synthetic.g").Missed; m != 9 {
		t.Errorf("Missed = %d, want 9 for real downtime", m)
	}
}
