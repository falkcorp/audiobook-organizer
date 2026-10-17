// file: internal/scheduler/v3_schedule_test.go
// version: 1.0.0
// guid: 5a90d3e7-2c18-4b64-8e0f-71b6c4a9d235
// last-edited: 2026-10-17

package scheduler

import (
	"context"
	"encoding/json"
	"slices"
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
		Common: ops.Common{Title: id, Help: "synthetic", Effects: e, Permission: auth.PermSettingsManage, Schedule: s},
		Run:    func(*ops.RC, ops.NoParams) (ops.NoResult, error) { return ops.NoResult{}, nil }})
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
	if !slices.Equal(got[:len(before)], before) {
		t.Fatal("existing tasks' window order changed")
	}
	if tail := got[len(before):]; !slices.Equal(tail, []string{"synthetic.first", "synthetic.a", "synthetic.b"}) {
		t.Fatalf("v3 window order = %v, want order then def id", tail)
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
	for i := 0; i < 5; i++ {
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
