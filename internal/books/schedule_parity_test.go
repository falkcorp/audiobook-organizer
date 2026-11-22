// file: internal/books/schedule_parity_test.go
// version: 0.1.0
// guid: bfc405a0-e9cc-4059-9d45-f314ebecb1b9
// last-edited: 2026-11-21

package books

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/kindrename"
	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m4"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	dbmocks "github.com/falkcorp/audiobook-organizer/internal/books/store/mocks"
	hconfig "github.com/falkcorp/audiobook-organizer/internal/config"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/scheduler"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/kernel"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/keys"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest/schedule"
)

// The schedule oracle (F5-14). HEAD's scheduler (internal/scheduler, the real
// TaskDefinitions behind NewTaskScheduler) runs on a fake clock beside core's
// scheduler (the taskstest/schedule harness) over one stub definition per
// books.ScheduledKinds() kind, each carrying NewSchedules' schedule. Both read
// one HEAD-shaped config per row: HEAD reads it as config.AppConfig, and core
// reads what M2 makes of it (cutover.Table's rows through cutover.Convert),
// plus schedulerDefaults() for the keys HEAD had no setting for. A step is one
// instant; at each, the two must enqueue the same kinds with the same triggers,
// open the window with the same members in the same order, and leave every
// clock HEAD keeps at the same instant.

// parityZone is the zone both schedulers run in: west of UTC, so a local date
// is not the UTC one.
var parityZone = time.FixedZone("Gotham", -5*3600)

// parityStart is a Monday, 12:00 Gotham.
var parityStart = time.Date(2030, 3, 4, 12, 0, 0, 0, parityZone)

// fullSweepSeedLag is how much later HEAD seeds the weekly sweep's clock on
// a store with no sweep on record: its due check's interval
// (scheduled.library_scan_full.interval, 60 minutes in every row here).
const fullSweepSeedLag = time.Hour

// knownRephase names difference (l) in parityResult.known: a start run at a
// restart re-anchors the kind's interval on core, not on HEAD.
const knownRephase = "an on-start kind's interval re-anchored at a restart"

// ---- task <-> kind ----

// taskKind is the books kind HEAD's task fires after the cutover, and false
// for a task whose kind is core's (batch_poller, purge_old_logs) or dropped
// (transcode). db_optimize is books' half of the split def,
// books.maintenance.aux-store-compact (core.storage.compact is core's).
func taskKind(task string) (tasks.Kind, bool) {
	if task == "db_optimize" {
		return "books.maintenance.aux-store-compact", true
	}
	tg, ok := kindrename.SchedulerKind(task)
	if !ok || tg.Dropped || strings.HasPrefix(string(tg.Kind), "core.") {
		return "", false
	}
	return tg.Kind, true
}

// ---- the config both sides read ----

// paritySetup is one row's inputs besides the HEAD config.
type paritySetup struct {
	dedup, activity, metadata bool
	// identPage is the identification dirty set's first page (HEAD's
	// identDirtySetNonEmpty); nil leaves the store without the capability.
	identPage func() ([]database.IdentDirtyEntry, error)
	// collections is CollectionsRefreshPending on both sides (nil: no check).
	collections func() (bool, error)
}

func (s paritySetup) has(b bool) func() bool { return func() bool { return b } }

// identPending is ScheduleDeps.IdentificationPending for s: what HEAD's
// identDirtySetNonEmpty answers.
func (s paritySetup) identPending() func() (bool, error) {
	if s.identPage == nil {
		return nil
	}
	return func() (bool, error) {
		page, err := s.identPage()
		if err != nil {
			return false, err
		}
		return len(page) > 0, nil
	}
}

// headTree is c as a JSON tree.
func headTree(t testing.TB, c hconfig.Config) map[string]any {
	t.Helper()
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// treeAt is the value at dotted path head in tree.
func treeAt(tree map[string]any, head string) (any, bool) {
	var cur any = tree
	for p := range strings.SplitSeq(head, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[p]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// withHead is c with the dotted HEAD paths set (the JSON names config.Config
// marshals to, which are the rename table's HEAD names).
func withHead(t testing.TB, c hconfig.Config, set map[string]any) hconfig.Config {
	t.Helper()
	tree := headTree(t, c)
	for head, v := range set {
		parts := strings.Split(head, ".")
		m := tree
		for _, p := range parts[:len(parts)-1] {
			next, ok := m[p].(map[string]any)
			if !ok {
				t.Fatalf("withHead: %s is not under an object", head)
			}
			m = next
		}
		if _, ok := m[parts[len(parts)-1]]; !ok {
			t.Fatalf("withHead: %s is not a HEAD config path", head)
		}
		m[parts[len(parts)-1]] = v
	}
	raw, err := json.Marshal(tree)
	if err != nil {
		t.Fatal(err)
	}
	var out hconfig.Config
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// coreView is what core reads for one HEAD config: every schedule key a
// declaration names and every gate input.
type coreView struct {
	schedules map[string]tasks.ScheduleConfig
	inputs    map[config.Path]json.RawMessage
	window    schedule.Window
}

// coreViewOf converts head the way M2 does: every key target of the rename
// table that names a schedule key or a gate input takes cutover.Convert of
// the row's HEAD value. A schedule key no row names is one of the keys HEAD
// had no setting for and takes schedulerDefaults(); a gate input no row names
// fails the test.
func coreViewOf(t testing.TB, head hconfig.Config) coreView {
	t.Helper()
	tree := headTree(t, head)
	want := map[string]bool{}
	for _, d := range scheduleDecls {
		want[scheduleKeyPrefix+d.key] = true
	}
	for _, p := range scheduleGateInputs {
		want[string(p)] = true
	}
	v := coreView{schedules: map[string]tasks.ScheduleConfig{}, inputs: map[config.Path]json.RawMessage{}}
	for _, r := range cutover.Table().Rows {
		for _, tg := range r.Targets {
			if tg.Kind != cutover.TargetKey || !want[tg.Path] {
				continue
			}
			hv, ok := treeAt(tree, r.Head)
			if !ok {
				continue
			}
			in, err := json.Marshal(hv)
			if err != nil {
				t.Fatal(err)
			}
			out, _, err := cutover.Convert(tg, in)
			if err != nil {
				t.Fatalf("convert %s -> %s: %v", r.Head, tg.Path, err)
			}
			if key, ok := strings.CutPrefix(tg.Path, scheduleKeyPrefix); ok {
				var sc tasks.ScheduleConfig
				if err := json.Unmarshal(out, &sc); err != nil {
					t.Fatalf("%s -> %s: %s is no ScheduleConfig: %v", r.Head, tg.Path, out, err)
				}
				v.schedules[scheduleKeyPrefix+key] = sc
				continue
			}
			v.inputs[config.Path(tg.Path)] = out
		}
	}
	defaults := map[string]tasks.ScheduleConfig{}
	raw, err := json.Marshal(schedulerDefaults().Schedules)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &defaults); err != nil {
		t.Fatal(err)
	}
	for _, d := range scheduleDecls {
		if _, ok := v.schedules[scheduleKeyPrefix+d.key]; ok {
			continue
		}
		sc, ok := defaults[d.key]
		if !ok {
			t.Fatalf("schedule key %s: no rename-table row and no schedulerDefaults() entry", d.key)
		}
		v.schedules[scheduleKeyPrefix+d.key] = sc
	}
	for _, p := range scheduleGateInputs {
		if _, ok := v.inputs[p]; !ok {
			t.Fatalf("gate input %s: no rename-table row converts a HEAD value into it", p)
		}
	}
	// A gate may read its own schedule key (scheduleOn), as the module's
	// config reads serve every declared key.
	for key, sc := range v.schedules {
		raw, err := json.Marshal(sc)
		if err != nil {
			t.Fatal(err)
		}
		v.inputs[config.Path(key)] = raw
	}
	m := head.Maintenance
	v.window = schedule.Window{Start: m.WindowStart, End: m.WindowEnd, Enabled: m.Enabled}
	return v
}

// fakeReads is ScheduleDeps.Config over a coreView's inputs, swappable at a
// step (an edit).
type fakeReads struct {
	mu sync.Mutex
	in map[config.Path]json.RawMessage
}

func (f *fakeReads) set(in map[config.Path]json.RawMessage) {
	f.mu.Lock()
	f.in = in
	f.mu.Unlock()
}

func (f *fakeReads) Lookup(p config.Path) (config.KeyState, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.in[p]
	return config.KeyState{Path: p, Effective: v}, ok
}

// ---- HEAD's side ----

// headRecord is one thing HEAD's scheduler did at a step.
type headRecord struct {
	task    string // the task a TriggerFn ran for ("" for a window)
	trigger tasks.Trigger
	members []string // a window's eligible tasks, in maintenance order
	params  json.RawMessage
}

// headRig is HEAD's TaskScheduler on a fake clock: SchedulerDeps.Now reads
// now and every SchedulerDeps.Tick call hands back an unbuffered channel the
// rig keeps, so a step can deliver the instant to every checker and know each
// has acted on it.
type headRig struct {
	t     *testing.T
	setup paritySetup

	mu       sync.Mutex
	now      time.Time
	timers   []chan time.Time
	settings map[string]string
	inserted map[string]database.OperationV2Row // by operation ID
	records  []headRecord
	booting  map[string]bool // the tasks whose fires are this boot's on-start runs

	store    *dbmocks.MockStore
	reg      *opsregistry.Registry
	ts       *scheduler.TaskScheduler
	shutdown chan struct{}
	wg       *sync.WaitGroup
}

// identStore is the mock store with the identification dirty set's page
// (database.PebbleStore.IdentDirtyPage), which identDirtySetNonEmpty finds by
// capability.
type identStore struct {
	*dbmocks.MockStore
	page func() ([]database.IdentDirtyEntry, error)
}

func (s identStore) IdentDirtyPage(string, int) ([]database.IdentDirtyEntry, error) { return s.page() }

func newHeadRig(t *testing.T, setup paritySetup) *headRig {
	t.Helper()
	r := &headRig{t: t, setup: setup, settings: map[string]string{}, inserted: map[string]database.OperationV2Row{}}
	st := dbmocks.NewMockStore(t)
	st.EXPECT().GetSetting(mock.Anything).RunAndReturn(func(key string) (*database.Setting, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		v, ok := r.settings[key]
		if !ok {
			return nil, database.ErrSettingNotFound
		}
		return &database.Setting{Key: key, Value: v, Type: "string"}, nil
	}).Maybe()
	st.EXPECT().SetSetting(mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		RunAndReturn(func(key, value, _ string, _ bool) error {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.settings[key] = value
			return nil
		}).Maybe()
	st.EXPECT().UpsertOpDefinitionV2(mock.Anything).Return(nil).Maybe()
	st.EXPECT().InsertOperationV2(mock.Anything).RunAndReturn(func(row database.OperationV2Row) error {
		r.mu.Lock()
		r.inserted[row.ID] = row
		r.mu.Unlock()
		if row.DefID == "maintenance.window" {
			r.recordWindow()
		}
		return nil
	}).Maybe()
	st.EXPECT().GetOperationV2(mock.Anything).Return(nil, nil).Maybe()
	st.EXPECT().ListActiveOperationsV2().Return(nil, nil).Maybe()
	st.EXPECT().GetDepRev(mock.Anything).Return(0, nil).Maybe()
	r.store = st
	r.reg = opsregistry.New(st, slog.New(slog.DiscardHandler), 1, nil)
	ids := map[string]bool{"maintenance.window": true}
	for _, id := range scheduler.TaskV2DefIDs() {
		ids[id] = true
	}
	for _, id := range slices.Sorted(maps.Keys(ids)) {
		if err := r.reg.RegisterOp(opsregistry.OperationDef{
			ID: id, Plugin: "gotham", ResumePolicy: opsregistry.ResumeDrop, Liveness: opsregistry.LivenessManual,
			Run: func(context.Context, json.RawMessage, opsregistry.Reporter) error { return nil },
		}); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}
	return r
}

func (r *headRig) clock() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.now
}

func (r *headRig) tick(time.Duration) (<-chan time.Time, func()) {
	ch := make(chan time.Time)
	r.mu.Lock()
	r.timers = append(r.timers, ch)
	r.mu.Unlock()
	return ch, func() {}
}

func (r *headRig) schedStore() scheduler.SchedulerStore {
	if r.setup.identPage != nil {
		return identStore{MockStore: r.store, page: r.setup.identPage}
	}
	return r.store
}

// recordWindow records a window opening: the members are what the
// maintenance.window op would run, filtered as the op filters them
// (internal/server/scheduler_maintenance_window_op.go:105-116: IsEnabled and
// RunInMaintenanceWindow over MaintenanceOrder; HEAD's running-task skip never
// applies here, the mock store lists no active operation).
func (r *headRig) recordWindow() {
	defs := r.ts.Tasks()
	var members []string
	for _, name := range r.ts.MaintenanceOrder() {
		d, ok := defs[name]
		if ok && d.IsEnabled() && d.RunInMaintenanceWindow != nil && d.RunInMaintenanceWindow() {
			members = append(members, name)
		}
	}
	r.mu.Lock()
	r.records = append(r.records, headRecord{trigger: tasks.TriggerWindow, members: members})
	r.mu.Unlock()
}

// boot builds a TaskScheduler over the rig (a restart when one runs: it is
// stopped first), wraps every TriggerFn so a fire is recorded with the task's
// name, starts it and waits for its timers and its on-start runs.
func (r *headRig) boot(at time.Time) {
	t := r.t
	t.Helper()
	r.stop()
	r.mu.Lock()
	r.now, r.timers = at, nil
	r.mu.Unlock()
	cfg := hconfig.Snapshot()
	ts := scheduler.NewTaskScheduler(scheduler.SchedulerDeps{
		Maintenance:               scheduler.MaintenanceSettings{Enabled: cfg.Maintenance.Enabled, WindowStart: cfg.Maintenance.WindowStart, WindowEnd: cfg.Maintenance.WindowEnd},
		Store:                     r.schedStore,
		OpRegistry:                r.reg,
		HasDedupPlugin:            r.setup.has(r.setup.dedup),
		HasMetadataFetchSvc:       r.setup.has(r.setup.metadata),
		HasActivitySvc:            r.setup.has(r.setup.activity),
		HasBatchPoller:            r.setup.has(false),
		CollectionsRefreshPending: r.setup.collections,
		Now:                       r.clock,
		Location:                  parityZone,
		Tick:                      r.tick,
	})
	r.ts = ts
	timers := 0
	booting := map[string]bool{}
	for name, d := range ts.Tasks() {
		if d.IsEnabled() && (d.DailyAt != "" || d.GetInterval() > 0) {
			timers++
		}
		if d.RunOnStart != nil && d.RunOnStart() && d.IsEnabled() {
			booting[name] = true
		}
		inner, task := d.TriggerFn, name
		d.TriggerFn = func(source string) (*database.Operation, error) {
			op, err := inner(source)
			if err != nil || op == nil {
				return op, err
			}
			r.mu.Lock()
			rec := headRecord{task: task, trigger: tasks.TriggerSchedule, params: json.RawMessage(r.inserted[op.ID].Params)}
			if r.booting[task] {
				rec.trigger = tasks.TriggerOnStart
				delete(r.booting, task)
			}
			r.records = append(r.records, rec)
			r.mu.Unlock()
			return op, nil
		}
	}
	if cfg.Maintenance.Enabled {
		timers++
	}
	r.mu.Lock()
	r.booting = booting
	r.mu.Unlock()
	r.shutdown, r.wg = make(chan struct{}), &sync.WaitGroup{}
	ts.Start(r.shutdown, r.wg)
	r.waitFor("timers and on-start runs", func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return len(r.timers) == timers && len(r.booting) == 0
	})
}

func (r *headRig) waitFor(what string, done func() bool) {
	r.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			r.mu.Lock()
			n, b := len(r.timers), slices.Sorted(maps.Keys(r.booting))
			r.mu.Unlock()
			r.t.Fatalf("HEAD's scheduler: no %s after 30s (%d timers, on-start pending %v)", what, n, b)
		}
		time.Sleep(time.Millisecond)
	}
}

// step delivers at to every timer, twice: a checker takes the second tick
// only after acting on the first, and a repeated check at one instant is
// idempotent.
func (r *headRig) step(at time.Time) {
	r.mu.Lock()
	r.now = at
	timers := slices.Clone(r.timers)
	r.mu.Unlock()
	for _, ch := range timers {
		ch <- at
		ch <- at
	}
}

func (r *headRig) stop() {
	if r.shutdown == nil {
		return
	}
	close(r.shutdown)
	r.wg.Wait()
	r.shutdown = nil
}

// take returns and clears what HEAD did since the last take.
func (r *headRig) take() []headRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.records
	r.records = nil
	return out
}

// clocks is every interval clock HEAD keeps, by the books kind it becomes
// (library_scan_full's is the sweep's library_scan_full_last_run: its tick is
// the hourly due check).
func (r *headRig) clocks() map[tasks.Kind]time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[tasks.Kind]time.Time{}
	for k, v := range r.settings {
		task, ok := strings.CutPrefix(k, "scheduled_task_last_tick.")
		if !ok || task == "library_scan_full" {
			continue
		}
		kind, ok := taskKind(task)
		if !ok {
			continue
		}
		at, err := time.Parse(time.RFC3339, v)
		if err != nil {
			r.t.Fatalf("%s holds %q", k, v)
		}
		out[kind] = at
	}
	if v, ok := r.settings["library_scan_full_last_run"]; ok {
		at, err := time.Parse(time.RFC3339, v)
		if err != nil {
			r.t.Fatalf("library_scan_full_last_run holds %q", v)
		}
		out[kindrename.FullSweepKind] = at
	}
	return out
}

// lines renders HEAD's records the way coreLines renders core's events.
func headLines(recs []headRecord) []string {
	var out []string
	for _, rec := range recs {
		if rec.trigger == tasks.TriggerWindow {
			var ms []string
			for _, m := range rec.members {
				if k, ok := taskKind(m); ok {
					ms = append(ms, string(k))
				}
			}
			out = append(out, "window ["+strings.Join(ms, " ")+"]")
			continue
		}
		if k, ok := taskKind(rec.task); ok {
			out = append(out, fmt.Sprintf("enqueue %s %s", k, rec.trigger))
		}
	}
	slices.Sort(out)
	return out
}

// ---- core's side ----

// stubDefs is one stub definition per scheduled kind with NewSchedules'
// schedule over deps.
func stubDefs(deps ScheduleDeps) []tasks.Definition {
	sched := NewSchedules(deps)
	var defs []tasks.Definition
	for _, k := range ScheduledKinds() {
		defs = append(defs, tasks.Task(k, tasks.TaskSpec[struct{}, struct{}]{
			Title: string(k), Effects: tasks.ReadOnly(), Resume: tasks.ResumeDrop,
			Perms: tasks.Permissions{Start: rbac.Need("gotham:run")}, Schedule: sched(k),
			Run: func(*tasks.RC, struct{}) (struct{}, error) { return struct{}{}, nil },
		}))
	}
	return defs
}

func coreLines(evs []schedule.Event) []string {
	var out []string
	for _, e := range evs {
		switch e.Type {
		case schedule.EventWindow:
			ms := make([]string, len(e.Members))
			for i, m := range e.Members {
				ms[i] = string(m)
			}
			out = append(out, "window ["+strings.Join(ms, " ")+"]")
		case schedule.EventEnqueue:
			if strings.HasPrefix(string(e.Kind), "core.window.") {
				continue // the window event stands for its run
			}
			out = append(out, fmt.Sprintf("enqueue %s %s", e.Kind, e.Trigger))
		}
	}
	slices.Sort(out)
	return out
}

// ---- one scenario on both sides ----

// parityStep is one instant of a scenario.
type parityStep struct {
	at   time.Time
	edit map[string]any // HEAD paths set before this step (with restart)
	boot bool           // a restart (an edit implies one: HEAD reads its timers' config at Start)
}

// parityScenario is a HEAD config, the deps and the steps.
type parityScenario struct {
	head  hconfig.Config
	setup paritySetup
	steps []parityStep
	// sweepLag compares core's scan-full an hour later (difference (c)):
	// set when the scenario starts with no sweep on record.
	sweepLag bool
	// settings are HEAD's settings rows before its first boot.
	settings map[string]string
	// pre is HEAD's history before the cutover: HEAD alone runs these
	// instants (one boot), then M4 imports its settings rows into core's
	// task store and both sides start at steps[0] (core with the imported
	// clocks, HEAD restarted over its own rows).
	pre []time.Time
}

// hourly is the steps every hour from parityStart for n hours.
func hourly(n int) []parityStep {
	out := make([]parityStep, n)
	for i := range out {
		out[i] = parityStep{at: parityStart.Add(time.Duration(i) * time.Hour)}
	}
	return out
}

// parityResult is what the scenario showed on each side, per step.
type parityResult struct {
	head, core [][]string
	headParams map[tasks.Kind][]json.RawMessage
	headTasks  map[string]int // HEAD fires by task name
	diffs      []string
	known      map[string]int // the recorded differences seen, by name
	imported   map[tasks.Kind]tasks.Clock
	headPre    []headRecord
	settings   map[string]string // HEAD's settings rows at the cutover
	defs       []tasks.Definition
	sched      func(tasks.Kind) *tasks.Schedule
}

// setHeadConfig makes cfg the process config HEAD's scheduler reads, under the
// config lock (Mutate with no Facade installed is a locked whole-struct write).
// Never `hconfig.AppConfig = cfg`: a rig's scheduler goroutines (the
// maintenance-window ticker, the on-start runs) read the config through
// Snapshot while the test moves between steps, and an unlocked assignment
// races them under -race (seen at -count=20 on the journal-prune parity test
// that reuses this rig). Not Project: it normalises, and the oracle must hand HEAD the
// scenario's values exactly.
func setHeadConfig(t *testing.T, cfg hconfig.Config) {
	t.Helper()
	if hconfig.Current() != nil {
		t.Fatal("a config Facade is installed: Mutate would write through it, not set HEAD's config")
	}
	hconfig.Mutate(func(c *hconfig.Config) { *c = cfg })
}

// runParity plays sc on both sides and reports every step where they differ.
func runParity(t *testing.T, sc parityScenario) parityResult {
	t.Helper()
	saved := hconfig.Snapshot()
	t.Cleanup(func() { setHeadConfig(t, saved) })

	head := sc.head
	setHeadConfig(t, head)
	view := coreViewOf(t, head)
	reads := &fakeReads{}
	reads.set(view.inputs)
	deps := ScheduleDeps{
		Config: reads, Log: slog.New(slog.DiscardHandler),
		HasDedupPlugin: sc.setup.has(sc.setup.dedup), HasActivitySvc: sc.setup.has(sc.setup.activity),
		HasMetadataFetchSvc:   sc.setup.has(sc.setup.metadata),
		IdentificationPending: sc.setup.identPending(), CollectionsRefreshPending: sc.setup.collections,
	}
	defs := stubDefs(deps)
	finish := slices.Clone(ScheduledKinds())
	finish = append(finish, tasks.WindowKind("nightly"))

	res := parityResult{headParams: map[tasks.Kind][]json.RawMessage{}, known: map[string]int{}, headTasks: map[string]int{}, defs: defs, sched: NewSchedules(deps)}
	rig := newHeadRig(t, sc.setup)
	defer rig.stop()
	maps.Copy(rig.settings, sc.settings)
	coreClock := map[tasks.Kind]time.Time{}
	if len(sc.pre) > 0 {
		rig.boot(sc.pre[0])
		for _, at := range sc.pre {
			rig.step(at)
		}
		res.headPre = rig.take()
		rig.stop()
		rig.mu.Lock()
		res.settings = maps.Clone(rig.settings)
		rig.mu.Unlock()
		res.imported = m4Import(t, res.settings, parityZone)
		for k, c := range res.imported {
			coreClock[k] = c.LastDue
		}
	}
	var steps []schedule.Step
	headClocks := make([]map[tasks.Kind]time.Time, len(sc.steps))
	stepViews := make([]coreView, len(sc.steps))
	cur := view
	for i, st := range sc.steps {
		boot := i == 0 || st.boot || st.edit != nil
		var edit func(*schedule.Config)
		if st.edit != nil {
			head = withHead(t, head, st.edit)
			setHeadConfig(t, head)
			next := coreViewOf(t, head)
			cur = next
			edit = func(c *schedule.Config) {
				c.Schedules = next.schedules
				c.Windows = map[string]schedule.Window{"nightly": next.window}
				reads.set(next.inputs)
			}
		}
		stepViews[i] = cur
		if boot {
			rig.boot(st.at)
		}
		rig.step(st.at)
		recs := rig.take()
		for _, rec := range recs {
			if rec.task != "" {
				res.headTasks[rec.task]++
			}
			if k, ok := taskKind(rec.task); ok && rec.params != nil {
				res.headParams[k] = append(res.headParams[k], rec.params)
			}
		}
		res.head = append(res.head, headLines(recs))
		headClocks[i] = rig.clocks()
		step := schedule.Step{At: st.at, Edit: edit, Finish: finish, Boot: boot && i > 0}
		if i == 0 && res.imported != nil {
			step.Clocks = maps.Clone(res.imported)
		}
		steps = append(steps, step)
	}
	rig.stop()
	// The harness reads gate inputs live, so an edit's inputs must be in place
	// for the step that applies it and not before: replay from the first view.
	reads.set(view.inputs)
	evs := schedule.Run(t, defs, schedule.Config{
		Schedules: view.schedules,
		Windows:   map[string]schedule.Window{"nightly": view.window},
		Poll:      time.Minute,
		Zone:      parityZone,
	}, steps)
	byStep := make([][]schedule.Event, len(sc.steps))
	for _, e := range evs {
		at := e.At
		// Difference (c): every scan-full event is compared an hour later
		// (see the clock check below); one past the last step is dropped.
		if sc.sweepLag && e.Kind == kindrename.FullSweepKind {
			at = at.Add(fullSweepSeedLag)
		}
		i := slices.IndexFunc(sc.steps, func(st parityStep) bool { return st.at.Equal(at) })
		if i < 0 {
			if sc.sweepLag && e.Kind == kindrename.FullSweepKind && at.After(sc.steps[len(sc.steps)-1].at) {
				continue
			}
			t.Fatalf("core event at %s, which is no step's instant: %s", e.At, e)
		}
		byStep[i] = append(byStep[i], e)
	}
	declared := map[tasks.Kind]bool{}
	for _, k := range ScheduledKinds() {
		declared[k] = true
	}
	// rephase is difference (l): the kinds whose interval core re-anchored at
	// a restart, each with the instant its clock now holds by the model.
	rephase := map[tasks.Kind]time.Time{}
	for i, st := range sc.steps {
		at := st.at.Format(time.RFC3339)
		lines := coreLines(byStep[i])
		res.core = append(res.core, lines)
		for _, e := range byStep[i] {
			if e.Type == schedule.EventSeed || e.Type == schedule.EventAdvance {
				coreClock[e.Kind] = e.LastDue
			}
		}
		// Difference (l), recorded in the PR: core's Boot stamps LastDue at
		// a start run on an enabled key, so a restart re-anchors that kind's
		// interval at the boot; HEAD's start run left its tick clock alone and
		// kept the old phase. It shows at every restart after the first (and
		// at the first after a cutover, whose clocks are HEAD's), from the
		// start run core was seen to make and stamp. At the first boot it
		// shows only where HEAD writes no clock at all: a window-only kind
		// (temp_file_cleanup) and library_scan started by scan_on_startup with
		// its timer off, whose stamped clock no poll advances.
		boot := i == 0 || st.boot || st.edit != nil
		if boot {
			for _, e := range byStep[i] {
				_, headHas := headClocks[i][e.Kind]
				if i == 0 && len(sc.pre) == 0 && headHas {
					continue
				}
				if e.Type == schedule.EventEnqueue && e.Trigger == tasks.TriggerOnStart && coreClock[e.Kind].Equal(st.at) {
					rephase[e.Kind] = st.at
					if i > 0 || len(sc.pre) > 0 {
						res.known[knownRephase]++
					} else {
						res.known["a first-boot start run stamped a clock HEAD never writes"]++
					}
				}
			}
		}
		// A re-anchored kind's scheduled fires follow core's model, not HEAD's
		// phase: one at the first step at or after the anchor plus its
		// interval while the key is on and its interval gate open (read with
		// this step's inputs), and its clock is the last of them.
		headL, coreL := slices.Clone(res.head[i]), slices.Clone(lines)
		reads.set(stepViews[i].inputs)
		for _, k := range slices.Sorted(maps.Keys(rephase)) {
			line := fmt.Sprintf("enqueue %s schedule", k)
			isLine := func(l string) bool { return l == line }
			fired := slices.Contains(coreL, line)
			headL, coreL = slices.DeleteFunc(headL, isLine), slices.DeleteFunc(coreL, isLine)
			d, _ := declFor(k)
			c := stepViews[i].schedules[scheduleKeyPrefix+d.key]
			every := time.Duration(c.Every)
			want := c.Enabled && every > 0 && res.sched(k).IntervalEnabled() && !st.at.Before(rephase[k].Add(every))
			if fired != want {
				res.diffs = append(res.diffs, fmt.Sprintf("%s: %s re-anchored at its restart: core fired %v, want %v (anchor %s + %s)",
					at, k, fired, want, rephase[k].Format(time.RFC3339), every))
			}
			if want {
				rephase[k] = st.at
			}
			if cc := coreClock[k]; !cc.Equal(rephase[k]) {
				res.diffs = append(res.diffs, fmt.Sprintf("%s: %s re-anchored: core clock %s, want %s", at, k,
					cc.Format(time.RFC3339), rephase[k].Format(time.RFC3339)))
			}
		}
		if !slices.Equal(coreL, headL) {
			res.diffs = append(res.diffs, fmt.Sprintf("%s: HEAD %v, core %v", at, headL, coreL))
		}
		// Every core clock of a declared kind is one HEAD holds too (a gate
		// that seeds a clock HEAD never wrote is a difference), but for the
		// re-anchored kinds and the recorded scan-full lag.
		for _, k := range slices.Sorted(maps.Keys(coreClock)) {
			if _, ok := headClocks[i][k]; ok || !declared[k] || rephase[k] != (time.Time{}) {
				continue
			}
			if sc.sweepLag && k == kindrename.FullSweepKind {
				res.known["scan-full seeded an hour earlier on core"]++
				continue
			}
			res.diffs = append(res.diffs, fmt.Sprintf("%s: core holds a clock for %s (LastDue %s), HEAD none", at, k,
				coreClock[k].UTC().Format(time.RFC3339)))
		}
		for _, k := range slices.Sorted(maps.Keys(headClocks[i])) {
			if _, ok := rephase[k]; ok {
				continue
			}
			hc := headClocks[i][k]
			cc, ok := coreClock[k]
			// Difference (c), recorded in the PR: with no sweep on record,
			// HEAD seeds the weekly sweep's clock at its first hourly due
			// check, an hour after boot; core seeds it at boot. Every sweep
			// after is then an hour earlier on core: its events are
			// compared at +fullSweepSeedLag above, and its clock here.
			if sc.sweepLag && ok && k == kindrename.FullSweepKind && hc.Equal(cc.Add(fullSweepSeedLag)) {
				res.known["scan-full seeded an hour earlier on core"]++
				continue
			}
			if !ok || !cc.Equal(hc) {
				res.diffs = append(res.diffs, fmt.Sprintf("%s: %s clock HEAD %s, core %s (%v)", st.at.Format(time.RFC3339), k,
					hc.UTC().Format(time.RFC3339), cc.UTC().Format(time.RFC3339), ok))
			}
		}
	}
	return res
}

// parityBase is HEAD's shipped defaults (the config oracle's golden) with the
// nightly window 01:00-05:00 on.
func parityBase(t *testing.T) hconfig.Config {
	t.Helper()
	return withHead(t, loadOracleGolden(t, "defaults").Config, map[string]any{
		"maintenance.enabled": true, "maintenance.window_start": 1, "maintenance.window_end": 5,
	})
}

// toggles is every HEAD switch a schedule reads set to on: each bool under
// scheduled.<task> (enabled, on_startup) and under maintenance (the window's
// own enabled among them), and the HEAD head of every bool gate input.
func toggles(t testing.TB, c hconfig.Config, on bool) map[string]any {
	t.Helper()
	tree := headTree(t, c)
	set := map[string]any{}
	for _, sect := range []string{"scheduled", "maintenance"} {
		m, _ := tree[sect].(map[string]any)
		for k, v := range m {
			switch v := v.(type) {
			case bool:
				set[sect+"."+k] = on
			case map[string]any:
				for kk, vv := range v {
					if _, ok := vv.(bool); ok {
						set[sect+"."+k+"."+kk] = on
					}
				}
			}
		}
	}
	gate := map[string]bool{}
	for _, p := range scheduleGateInputs {
		gate[string(p)] = true
	}
	for _, r := range cutover.Table().Rows {
		for _, tg := range r.Targets {
			if tg.Kind != cutover.TargetKey || !gate[tg.Path] {
				continue
			}
			if v, ok := treeAt(tree, r.Head); ok {
				if _, isBool := v.(bool); isBool {
					set[r.Head] = on
				}
			}
		}
	}
	return set
}

func declFor(k tasks.Kind) (scheduleDecl, bool) {
	for _, d := range scheduleDecls {
		if d.kind == k {
			return d, true
		}
	}
	return scheduleDecl{}, false
}

// headDryRun reports the dry_run HEAD's TriggerFn sent ("" when none).
func headDryRun(t testing.TB, raw json.RawMessage) string {
	t.Helper()
	var m map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &m) != nil {
		return ""
	}
	return string(m["dry_run"])
}

// withoutDryRun is raw with dry_run removed: core states the mode on the
// schedule, not in the params (difference (h)).
func withoutDryRun(t testing.TB, raw json.RawMessage) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("params %s: %v", raw, err)
	}
	delete(m, "dry_run")
	out, _ := json.Marshal(m)
	return string(out)
}

// TestScheduleParity_AllDefinitions: see the file comment. Rows: HEAD's
// defaults; every switch on; every switch off; library_scan's eight
// combinations of its three switches and a later restart with scan-on-startup
// off; catalog_harvest disabled and re-enabled by an edit; each idle gate
// empty, non-empty and failing; and purge with a zero retention.
func TestScheduleParity_AllDefinitions(t *testing.T) {
	type row struct {
		name  string
		set   map[string]any
		setup paritySetup
		steps []parityStep
	}
	all := paritySetup{dedup: true, activity: true, metadata: true}
	nonEmpty := func() ([]database.IdentDirtyEntry, error) { return []database.IdentDirtyEntry{{}}, nil }
	empty := func() ([]database.IdentDirtyEntry, error) { return nil, nil }
	failing := func() ([]database.IdentDirtyEntry, error) { return nil, fmt.Errorf("gotham vault sealed") }
	pending := func(b bool, err error) func() (bool, error) { return func() (bool, error) { return b, err } }
	base := parityBase(t)
	onSetup := all
	onSetup.identPage, onSetup.collections = nonEmpty, pending(true, nil)

	rows := []row{
		{name: "defaults", setup: all, steps: hourly(52)},
		// Eight days: the weekly kinds (label_refinement, the full sweep)
		// fire once.
		{name: "all on", set: toggles(t, base, true), setup: onSetup, steps: hourly(8*24 + 2)},
		{name: "all off", set: toggles(t, base, false), setup: paritySetup{}, steps: hourly(52)},
	}
	for _, periodic := range []bool{false, true} {
		for _, maint := range []bool{false, true} {
			for _, sos := range []bool{false, true} {
				rows = append(rows, row{
					name:  fmt.Sprintf("library_scan periodic=%v maint=%v sos=%v", periodic, maint, sos),
					set:   map[string]any{"scheduled.library_scan.enabled": periodic, "maintenance.library_scan": maint, "scan_on_startup": sos},
					setup: all, steps: hourly(30),
				})
			}
		}
	}
	sosOff := hourly(30)
	sosOff[3].edit = map[string]any{"scan_on_startup": false}
	rows = append(rows, row{name: "library_scan sos then off", setup: all, steps: sosOff,
		set: map[string]any{"scan_on_startup": true, "maintenance.library_scan": true}})
	catalog := hourly(40)
	catalog[8].edit = map[string]any{"catalog.enabled": false}
	catalog[20].edit = map[string]any{"catalog.enabled": true}
	rows = append(rows, row{name: "catalog_harvest off then on", setup: all, steps: catalog,
		set: map[string]any{"scheduled.catalog_harvest.enabled": true, "catalog.enabled": true}})
	for _, g := range []struct {
		name string
		page func() ([]database.IdentDirtyEntry, error)
	}{{"empty", empty}, {"non-empty", nonEmpty}, {"error", failing}} {
		s := all
		s.identPage = g.page
		rows = append(rows, row{name: "identification " + g.name, setup: s, steps: hourly(6),
			set: map[string]any{"identification_advance_enabled": true}})
	}
	for _, g := range []struct {
		name string
		fn   func() (bool, error)
	}{{"empty", pending(false, nil)}, {"non-empty", pending(true, nil)}, {"error", pending(false, fmt.Errorf("gotham vault sealed"))}} {
		s := all
		s.collections = g.fn
		rows = append(rows, row{name: "collections " + g.name, setup: s, steps: hourly(6)})
	}
	rows = append(rows, row{name: "purge retention 0", setup: all, steps: hourly(30),
		set: map[string]any{"purge_soft_deleted_after_days": 0}})
	// Each window toggle alone: everything on but one task's
	// RunInMaintenanceWindow switch (the rows above flip them all at once).
	for _, tg := range []string{"library_scan", "library_organize", "library_size_refresh", "dedup_refresh", "series_prune",
		"author_split", "db_optimize", "purge_deleted", "tombstone_cleanup", "reconcile"} {
		set := toggles(t, base, true)
		set["maintenance."+tg] = false
		rows = append(rows, row{name: "all on but maintenance." + tg, set: set, setup: onSetup, steps: hourly(30)})
	}
	// The tasks whose HEAD timer needs a positive interval, which HEAD's
	// defaults leave at 0 (toggles sets only switches).
	intervals := toggles(t, base, true)
	for _, task := range []string{"metadata_refresh", "metadata_upgrade", "resolve_production_authors"} {
		intervals["scheduled."+task+".enabled"] = true
		intervals["scheduled."+task+".interval"] = 120
	}
	rows = append(rows, row{name: "interval-only tasks at 2h", set: intervals, setup: onSetup, steps: hourly(12)})

	headFired := map[tasks.Kind]bool{}
	coreFired := map[tasks.Kind]bool{}
	headTasks := map[string]int{}
	for _, rw := range rows {
		t.Run(rw.name, func(t *testing.T) {
			head := base
			if rw.set != nil {
				head = withHead(t, head, rw.set)
			}
			res := runParity(t, parityScenario{head: head, setup: rw.setup, steps: rw.steps, sweepLag: true})
			for _, d := range res.diffs {
				t.Error(d)
			}
			// A row that restarts shows difference (l): the on-start kinds
			// (purge_deleted, temp_file_cleanup, transcode_temp_cleanup) run
			// at every boot under HEAD's defaults.
			if slices.ContainsFunc(rw.steps[1:], func(st parityStep) bool { return st.boot || st.edit != nil }) && res.known[knownRephase] == 0 {
				t.Errorf("the row restarts but no on-start kind was re-anchored: the model of difference (l) went unexercised")
			}
			for task, n := range res.headTasks {
				headTasks[task] += n
			}
			// A kind is reached when it is enqueued or is a member of a window
			// run (the window event stands for its members' starts).
			for _, side := range []struct {
				lines [][]string
				into  map[tasks.Kind]bool
			}{{res.head, headFired}, {res.core, coreFired}} {
				for _, step := range side.lines {
					for _, l := range step {
						switch f := strings.Fields(l); f[0] {
						case "enqueue":
							side.into[tasks.Kind(f[1])] = true
						case "window":
							for _, m := range f[1:] {
								side.into[tasks.Kind(strings.Trim(m, "[]"))] = true
							}
						}
					}
				}
			}
			// The run mode: core's schedule states the mode HEAD's params
			// carried (dry_run=false is live; a preview op was sent none).
			for k, params := range res.headParams {
				d, ok := declFor(k)
				if !ok {
					t.Errorf("HEAD fired %s, which no declaration names", k)
					continue
				}
				// HEAD's live ops mostly send no dry_run (they are live
				// without one), so only a stated dry_run is checked.
				for _, p := range params {
					switch dr := headDryRun(t, p); {
					case dr == "false" && d.mode != modeLive, dr == "true" && d.mode != modePreview:
						t.Errorf("%s: HEAD sent params %s, the declaration's mode is %d", k, p, d.mode)
					}
				}
				sc := res.sched(k)
				if sc.IsLive() != (d.mode == modeLive) || sc.IsPreview() != (d.mode == modePreview) {
					t.Errorf("%s: schedule live=%v preview=%v, declaration mode %d", k, sc.IsLive(), sc.IsPreview(), d.mode)
				}
				// A ParamsFunc resolves to what HEAD's TriggerFn sent
				// (both read the same config).
				if sc.HasParamsFunc() && len(params) > 0 {
					got, err := sc.ResolveParams()
					if err != nil {
						t.Fatalf("%s: ResolveParams: %v", k, err)
					}
					if w, g := withoutDryRun(t, params[len(params)-1]), withoutDryRun(t, got); w != g {
						t.Errorf("%s: HEAD params %s, core resolves %s", k, w, g)
					}
				}
			}
		})
	}

	// Coverage: transcode (dropped) and series_normalize (a constant false
	// in HEAD, off by default in core) never start; acoustid_online_lookup
	// had no reachable path in HEAD and is off by default in core; every
	// other scheduled kind fired on both sides in some row.
	for _, task := range []string{"transcode", "series_normalize", "acoustid_online_lookup"} {
		if headTasks[task] > 0 {
			t.Errorf("HEAD started %s %d times", task, headTasks[task])
		}
	}
	never := map[tasks.Kind]bool{"books.dedup.series-normalize": true, "books.acoustid.lookup-online": true}
	for _, k := range ScheduledKinds() {
		if never[k] {
			if coreFired[k] {
				t.Errorf("core started %s", k)
			}
			continue
		}
		if !headFired[k] || !coreFired[k] {
			t.Errorf("%s: fired on HEAD %v, on core %v, in no row on both: the oracle does not cover it", k, headFired[k], coreFired[k])
		}
	}
}

// ---- the declarations themselves ----

// scheduleSegment is the config path grammar every schedule key segment
// obeys (core/internal/schedspec.Segment, which books cannot import).
var scheduleSegment = regexp.MustCompile(`^[a-z][a-z0-9_]{0,47}$`)

// TestScheduleFor_KeysDeclaredAndValid: every declaration's key has the R2-7
// form modules.books.<subsystem>.schedules.<name>, is declared in booksSettings()
// with type tasks.ScheduleConfig, and names one kind; every declared schedule
// key is used; NewSchedules returns nil for core's kinds; and every gate input
// is a declared key (books' in booksSettings(), core.ai.journal_retention in
// the kernel's core section list).
func TestScheduleFor_KeysDeclaredAndValid(t *testing.T) {
	b := newBooksStore(t)
	sched := NewSchedules(ScheduleDeps{})
	fieldType := map[string]reflect.Type{}
	st := reflect.TypeOf(schedulerDefaults().Schedules)
	for i := range st.NumField() {
		f := st.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		fieldType[name] = f.Type
	}
	byKey := map[string]tasks.Kind{}
	for _, d := range scheduleDecls {
		key := scheduleKeyPrefix + d.key
		segs := strings.Split(key, ".")
		if len(segs) != 5 || segs[0] != "modules" || segs[1] != "books" || segs[3] != "schedules" || segs[2] == "schedules" {
			t.Errorf("%s: key %s is not modules.books.<subsystem>.schedules.<name>", d.kind, key)
		}
		for _, s := range segs {
			if !scheduleSegment.MatchString(s) {
				t.Errorf("%s: key %s segment %q breaks [a-z][a-z0-9_]{0,47}", d.kind, key, s)
			}
		}
		if got := sched(d.kind); got == nil || got.ConfigKey() != key {
			t.Errorf("%s: NewSchedules gives %v, want a schedule over %s", d.kind, got, key)
		}
		if _, ok := b.keys[config.Path(key)]; !ok {
			t.Errorf("%s: key %s is not declared in booksSettings()", d.kind, key)
		}
		if ft := fieldType[d.key]; ft != reflect.TypeFor[tasks.ScheduleConfig]() {
			t.Errorf("%s: key %s is declared with type %v, want tasks.ScheduleConfig", d.kind, key, ft)
		}
		if other, dup := byKey[key]; dup {
			t.Errorf("key %s is on %s and %s: one key, one kind", key, other, d.kind)
		}
		byKey[key] = d.kind
	}
	for p := range b.keys {
		if strings.HasPrefix(string(p), scheduleKeyPrefix) {
			if _, used := byKey[string(p)]; !used {
				t.Errorf("schedule key %s is declared but no declaration reads it", p)
			}
		}
	}
	core := map[tasks.Kind]bool{tasks.WindowKind("nightly"): true}
	for _, k := range kindrename.CoreKinds() {
		core[k] = true
	}
	for task := range scheduler.TaskV2DefIDs() {
		if tg, ok := kindrename.SchedulerKind(task); ok && strings.HasPrefix(string(tg.Kind), "core.") {
			core[tg.Kind] = true
		}
	}
	for k := range core {
		if s := sched(k); s != nil {
			t.Errorf("NewSchedules(%s) = %v, want nil for a core kind", k, s)
		}
	}
	for _, p := range scheduleGateInputs {
		if strings.HasPrefix(string(p), "modules.books.") {
			if _, ok := b.keys[p]; !ok {
				t.Errorf("gate input %s is not declared in booksSettings()", p)
			}
			continue
		}
		section, leaf, _ := cutLast(string(p))
		if !coreSectionDeclares(t, section, leaf) {
			t.Errorf("gate input %s: the kernel's core section list declares no %s in %s", p, leaf, section)
		}
	}
}

func cutLast(p string) (string, string, bool) {
	i := strings.LastIndex(p, ".")
	if i < 0 {
		return "", p, false
	}
	return p[:i], p[i+1:], true
}

// coreSectionDeclares reports whether core/kernel/core_sections.go defines
// section (config.Define("<section>", <T>{...})) with a field of JSON name
// leaf in <T>. The list is unexported (coreSections), so it is read as source.
func coreSectionDeclares(t *testing.T, section, leaf string) bool {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(wd, "..", "..", "core", "kernel", "core_sections.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var typeName string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) < 2 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Define" {
			return true
		}
		if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Value == strconv.Quote(section) {
			if cl, ok := call.Args[1].(*ast.CompositeLit); ok {
				if id, ok := cl.Type.(*ast.Ident); ok {
					typeName = id.Name
				}
			}
		}
		return true
	})
	if typeName == "" {
		return false
	}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != typeName {
			return true
		}
		stt, ok := ts.Type.(*ast.StructType)
		if !ok {
			return false
		}
		for _, fld := range stt.Fields.List {
			if fld.Tag == nil {
				continue
			}
			tag, _ := strconv.Unquote(fld.Tag.Value)
			name, _, _ := strings.Cut(reflect.StructTag(tag).Get("json"), ",")
			if name == leaf {
				found = true
			}
		}
		return false
	})
	return found
}

// droppedV2Crons are HEAD's 15 v2 OperationDef.Schedule strings (Q-C, D15):
// none was ever evaluated. The 14 a TaskDefinition fires run at that task's
// cadence through its key; deluge.protected-paths-sync is manual.
var droppedV2Crons = []struct{ defID, cron, task string }{
	{"deluge.protected-paths-sync", "*/30 * * * *", ""},
	{"maintenance.metadata-refresh", "0 6 * * *", "metadata_refresh"},
	{"maintenance.file-integrity-check", "30 2 * * *", "file_integrity_check"},
	{"maintenance.nightly-compact-activity-log", "10 0 * * *", "nightly_activity_compaction"},
	{"maintenance.db-optimize", "0 2 * * 0", "db_optimize"},
	{"maintenance.orphan-book-files-cleanup", "15 2 * * *", "orphan_book_files_cleanup"},
	{"maintenance.purge-deleted", "0 3 * * *", "purge_deleted"},
	{"maintenance.tombstone-cleanup", "0 4 * * *", "tombstone_cleanup"},
	{"maintenance.temp-file-cleanup", "30 1 * * *", "temp_file_cleanup"},
	{"maintenance.cleanup-activity-log", "0 0 * * *", "cleanup_activity_log"},
	{"maintenance.purge-old-logs", "0 2 * * 0", "purge_old_logs"},
	{"maintenance.cleanup-old-backups", "0 5 * * *", "cleanup_old_backups"},
	{"maintenance.trash-cleanup", "0 6 * * *", "trash_cleanup"},
	{"maintenance.author-split-scan", "0 2 * * 1", "author_split_scan"},
	{"maintenance.ai-dedup-batch", "0 0 * * *", "ai_dedup_batch"},
}

// TestScheduleFor_DroppedV2CronsAbsent: no schedule key's default carries a
// cron at all (so none of the 15); books.deluge.protected-paths-sync has no
// schedule; each of the 14 others is the def its task fires, and its kind is
// scheduled only through that task's key (purge_old_logs's kind is core's, so
// books schedules nothing for it).
func TestScheduleFor_DroppedV2CronsAbsent(t *testing.T) {
	if len(droppedV2Crons) != 15 {
		t.Fatalf("%d dropped crons listed, want 15", len(droppedV2Crons))
	}
	defaults := map[string]tasks.ScheduleConfig{}
	raw, _ := json.Marshal(schedulerDefaults().Schedules)
	if err := json.Unmarshal(raw, &defaults); err != nil {
		t.Fatal(err)
	}
	for key, sc := range defaults {
		if sc.Cron != "" {
			t.Errorf("schedule key %s defaults to cron %q: no v2 cron survives (Q-C)", key, sc.Cron)
		}
	}
	sched := NewSchedules(ScheduleDeps{})
	defIDs := scheduler.TaskV2DefIDs()
	for _, c := range droppedV2Crons {
		if c.task == "" {
			for task, id := range defIDs {
				if id == c.defID {
					t.Errorf("%s: task %s fires it; the list says no task does", c.defID, task)
				}
			}
			if s := sched("books." + tasks.Kind(c.defID)); s != nil {
				t.Errorf("books.%s has a schedule; it is manual", c.defID)
			}
			continue
		}
		if got := defIDs[c.task]; got != c.defID {
			t.Errorf("%s: task %s fires %q", c.defID, c.task, got)
		}
		kind, books := taskKind(c.task)
		if !books {
			continue // core's kind (purge_old_logs): core declares its schedule
		}
		d, ok := declFor(kind)
		if !ok || d.task != c.task {
			t.Errorf("%s: kind %s is not declared by task %s's row", c.defID, kind, c.task)
			continue
		}
		if s := sched(kind); s == nil || s.ConfigKey() != scheduleKeyPrefix+d.key {
			t.Errorf("%s: kind %s is scheduled by %v, want only key %s", c.defID, kind, s, d.key)
		}
	}
}

// scheduledKindsGolden is ScheduledKinds(), sorted.
var scheduledKindsGolden = []tasks.Kind{
	"books.acoustid.backfill",
	"books.acoustid.lookup-online",
	"books.catalog.harvest-authors",
	"books.collections.refresh-relative-dynamic",
	"books.dedup.author-scan",
	"books.dedup.label-refinement",
	"books.dedup.llm-review",
	"books.dedup.series-normalize",
	"books.dedup.series-prune",
	"books.identification.advance",
	"books.library.organize",
	"books.library.scan",
	"books.library.scan-full",
	"books.library.size-refresh",
	"books.maintenance.ai-dedup-batch",
	"books.maintenance.author-split-scan",
	"books.maintenance.aux-store-compact",
	"books.maintenance.cleanup-activity-log",
	"books.maintenance.cleanup-old-backups",
	"books.maintenance.file-integrity-check",
	"books.maintenance.metadata-refresh",
	"books.maintenance.nightly-compact-activity-log",
	"books.maintenance.orphan-book-files-cleanup",
	"books.maintenance.prune-ai-journal",
	"books.maintenance.purge-deleted",
	"books.maintenance.reports-prune",
	"books.maintenance.resolve-production-authors",
	"books.maintenance.temp-file-cleanup",
	"books.maintenance.tombstone-cleanup",
	"books.maintenance.transcode-temp-cleanup",
	"books.maintenance.trash-cleanup",
	"books.metadata.candidate-fetch",
	"books.metafetch.asin-backfill",
	"books.reconcile.scan",
	"books.scheduler.metadata-upgrade",
}

// TestScheduleFor_KindListMatchesConversions: ScheduledKinds() is the golden
// list, and each kind is declared by exactly one non-test file under
// internal/plugins, internal/server or internal/scheduler, in one of the forms
// the conversions use: the literal "books.<id>", tasks.Kind("books." + with
// "<id>", or t7KindFor( with "<id>".
func TestScheduleFor_KindListMatchesConversions(t *testing.T) {
	if got := ScheduledKinds(); !slices.Equal(got, scheduledKindsGolden) {
		t.Fatalf("ScheduledKinds() = %v\nwant %v", got, scheduledKindsGolden)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	decls := kindDeclarations(t, filepath.Join(wd, "..", ".."), "internal/plugins", "internal/server", "internal/scheduler")
	for _, k := range scheduledKindsGolden {
		if in := decls[k]; len(in) != 1 {
			t.Errorf("%s is declared by %d files, want 1: %v", k, len(in), in)
		}
	}
}

// kindDeclarations maps each books kind to the non-test files under dirs (of
// root) that declare it: a const or var of type tasks.Kind, a tasks.Kind(x)
// conversion, or a t7KindFor(x) call, with x resolved through its package's
// string constants. A kind's literal in a NotWhile list, a tasks.Key or a
// comment names it without declaring it, and an argument that does not
// resolve (t7KindFor's own body) declares nothing.
func kindDeclarations(t *testing.T, root string, dirs ...string) map[tasks.Kind][]string {
	t.Helper()
	pkgs := map[string][]string{} // directory -> its non-test files
	for _, dir := range dirs {
		err := filepath.WalkDir(filepath.Join(root, dir), func(p string, de fs.DirEntry, err error) error {
			if err != nil || de.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return err
			}
			pkgs[filepath.Dir(p)] = append(pkgs[filepath.Dir(p)], p)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	isTasksKind := func(e ast.Expr) bool {
		s, ok := e.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		x, ok := s.X.(*ast.Ident)
		return ok && x.Name == "tasks" && s.Sel.Name == "Kind"
	}
	out := map[tasks.Kind][]string{}
	for _, dir := range slices.Sorted(maps.Keys(pkgs)) {
		fset := token.NewFileSet()
		files := map[string]*ast.File{}
		for _, p := range pkgs[dir] {
			f, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatalf("parse %s: %v", p, err)
			}
			files[p] = f
		}
		consts := map[string]string{}
		var fold func(ast.Expr) (string, bool)
		fold = func(e ast.Expr) (string, bool) {
			switch e := e.(type) {
			case *ast.BasicLit:
				if e.Kind != token.STRING {
					return "", false
				}
				s, err := strconv.Unquote(e.Value)
				return s, err == nil
			case *ast.Ident:
				s, ok := consts[e.Name]
				return s, ok
			case *ast.ParenExpr:
				return fold(e.X)
			case *ast.BinaryExpr:
				if e.Op != token.ADD {
					return "", false
				}
				x, okx := fold(e.X)
				y, oky := fold(e.Y)
				return x + y, okx && oky
			case *ast.CallExpr:
				if isTasksKind(e.Fun) && len(e.Args) == 1 {
					return fold(e.Args[0])
				}
			}
			return "", false
		}
		// The package's string constants, in any declaration order.
		for changed := true; changed; {
			changed = false
			for _, f := range files {
				for _, d := range f.Decls {
					g, ok := d.(*ast.GenDecl)
					if !ok || g.Tok != token.CONST {
						continue
					}
					for _, s := range g.Specs {
						vs := s.(*ast.ValueSpec)
						for i, n := range vs.Names {
							if _, done := consts[n.Name]; done || i >= len(vs.Values) {
								continue
							}
							if v, ok := fold(vs.Values[i]); ok {
								consts[n.Name], changed = v, true
							}
						}
					}
				}
			}
		}
		for _, p := range slices.Sorted(maps.Keys(files)) {
			found := map[tasks.Kind]bool{}
			record := func(s string, ok bool) {
				if ok && strings.HasPrefix(s, "books.") {
					found[tasks.Kind(s)] = true
				}
			}
			ast.Inspect(files[p], func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.ValueSpec:
					if n.Type != nil && isTasksKind(n.Type) {
						for _, v := range n.Values {
							record(fold(v))
						}
					}
				case *ast.CallExpr:
					if len(n.Args) != 1 {
						break
					}
					if isTasksKind(n.Fun) {
						record(fold(n.Args[0]))
					} else if id, ok := n.Fun.(*ast.Ident); ok && id.Name == "t7KindFor" {
						if s, ok := fold(n.Args[0]); ok {
							record("books."+s, true)
						}
					}
				}
				return true
			})
			rel, _ := filepath.Rel(root, p)
			for k := range found {
				out[k] = append(out[k], filepath.ToSlash(rel))
			}
		}
	}
	return out
}

// ---- the first boot after the cutover ----

// lateImporter is tasks.NativeImporter over core's task keyspace, bound after
// the store opens (M4's Config takes the importer before).
type lateImporter struct{ tasks.Importer }

// m4Import cuts a books store holding HEAD's settings rows over with M4 alone
// (books' storage spec raised to its cutover level behind its level step, M4's
// families retired, the kernel's data-migration runner and preflight, the way
// internal/books/cutover/m4's rig does) and returns the clock M4 imported for
// every scheduled kind, core.storage.compact, core.log.journal.prune and the
// nightly window.
func m4Import(t *testing.T, settings map[string]string, zone *time.Location) map[tasks.Kind]tasks.Clock {
	t.Helper()
	ctx := context.Background()
	const dir, path, ownerTasks = "/batcave/db", "/batcave/db/audiobooks.pebble", "core/tasks"
	discard := slog.New(slog.DiscardHandler)
	mem := vfs.NewCrashableMem()
	mem.TestingSetDiskUsage(vfs.DiskUsage{AvailBytes: 1 << 40, TotalBytes: 1 << 41, UsedBytes: 1 << 40})
	if err := mem.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	level := 0
	for _, m := range database.BooksSpec(nil).Migrations {
		level = max(level, m.Version)
	}
	level++
	owners := func(cut bool) []storage.OwnerSpec {
		spec := database.BooksSpec(nil)
		if cut {
			spec.Level = cutover.BooksCutoverLevel
			spec.Migrations = append(spec.Migrations, cutover.BooksLevelStep(level, cutover.BooksCutoverLevel))
			for i := range spec.Claims {
				for _, p := range m4.SourcePrefixes() {
					if strings.HasPrefix(spec.Claims[i].Prefix, p) {
						spec.Claims[i].Mode = storage.ClaimRetired
						break
					}
				}
			}
			for _, p := range m4.SourcePrefixes() {
				if strings.HasPrefix(p, "setting:") {
					spec.Claims = append(spec.Claims, storage.Claim{Prefix: p, Mode: storage.ClaimRetired, Description: "M4 source: " + p})
				}
			}
			for _, k := range m4.SourceKeys() {
				spec.Claims = append(spec.Claims, storage.Claim{Key: k, Mode: storage.ClaimRetired, Description: "M4 source: " + k})
			}
		}
		var ser []string
		for _, fam := range []string{"run", "debounce", "sched", "idem"} {
			ser = append(ser, string(keys.Prefix(keys.Str(fam))))
		}
		return []storage.OwnerSpec{
			{Owner: cutover.BooksOwner, Spec: spec, Enabled: true},
			{Owner: ownerTasks, Spec: storage.Spec{Keyspace: true, Serialized: ser}, Enabled: true},
		}
	}

	o, err := storage.Open(ctx, storage.OpenOptions{Path: path, FS: storage.FSOf(mem), Owners: owners(false), Build: "batcomputer-1", FreeSpaceMargin: 1})
	if err != nil {
		t.Fatalf("seed open: %v", err)
	}
	db, err := o.Prepare(ctx)
	if err == nil {
		err = db.Migrate(ctx)
	}
	if err != nil {
		t.Fatalf("seed boot: %v", errors.Join(err, o.Close()))
	}
	lr, _ := db.Legacy(cutover.BooksOwner)
	for k, v := range settings {
		row, err := json.Marshal(database.Setting{Key: k, Value: v, Type: "string"})
		if err != nil {
			t.Fatal(err)
		}
		if err := lr.Set(ctx, []byte("setting:"+k), row); err != nil {
			t.Fatalf("seed %s: %v", k, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	imp := &lateImporter{}
	var legacy storage.Reader
	dm, err := m4.New(m4.Config{
		Importer: imp, Legacy: func() storage.Reader { return legacy },
		ScanStandDownKey: "books.library.scan", StampAt: time.Date(2030, 3, 1, 0, 0, 0, 0, time.UTC), Location: zone,
		Reconcile: func(context.Context) error { return nil }, Logger: discard,
	}, cutover.TasksCutoverID, cutover.BooksCutoverLevel, []storage.StepRef{cutover.BooksLevelStepRef(level)})
	if err != nil {
		t.Fatal(err)
	}
	dms, err := kernel.NewDataMigrations(kernel.DataMigrationsConfig{ChunkItems: 64, Build: "batcomputer-2", Logger: discard,
		Quarantine: storage.QuarantineWriter(nil), Preflight: kernel.CutoverPreflight(discard)}, owners(true), dm)
	if err != nil {
		t.Fatal(err)
	}
	o, err = storage.Open(ctx, storage.OpenOptions{Path: path, FS: storage.FSOf(mem), Owners: owners(true), Build: "batcomputer-2",
		FreeSpaceMargin: 1, DataSteps: dms.Steps()})
	if err != nil {
		t.Fatalf("cutover open: %v", err)
	}
	if err := dms.Preflight(ctx, o); err != nil {
		t.Fatalf("preflight: %v", errors.Join(err, o.Close()))
	}
	db, err = o.Prepare(ctx)
	if err != nil {
		t.Fatalf("cutover prepare: %v", errors.Join(err, o.Close()))
	}
	defer func() { _ = db.Close() }()
	ks, _ := db.Keyspace(ownerTasks)
	imp.Importer = tasks.NativeImporter(ks)
	legacy, _ = db.Legacy(cutover.BooksOwner)
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("M4: %v", err)
	}
	clocks := tasks.NativeStores(ks).Clocks
	out := map[tasks.Kind]tasks.Clock{}
	for _, k := range append(ScheduledKinds(), "core.storage.compact", "core.log.journal.prune", tasks.WindowKind("nightly")) {
		c, ok, err := clocks.Get(ctx, k)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			out[k] = c
		}
	}
	return out
}

// TestCutover_NoCatchUpStorm: HEAD runs a day and a half (its window
// 23:00-24:00 Gotham, run once at 23:30 on the last evening; db_optimize on;
// the last weekly sweep 160 hours before the cutover), M4 imports its settings
// rows, and core's scheduler starts over the imported clocks at 23:45 beside
// HEAD restarted over its own rows, with metadata_refresh switched on at that
// restart so one enabled kind has no clock at all (seeded at the boot's
// first poll, first run one interval later). On that first boot only the on-start
// kinds fire; no kind fires before its next due instant from HEAD's clock
// (LastDue + every) and each fires exactly then: the full sweep at last_run +
// 168h, both db_optimize halves at the last tick + 24h, with no gap; the
// window, imported west of UTC as the run's local midnight, does not open
// again that evening and opens the next one at 23:00.
func TestCutover_NoCatchUpStorm(t *testing.T) {
	head := withHead(t, parityBase(t), map[string]any{
		"maintenance.window_start": 23, "maintenance.window_end": 0,
		"scheduled.db_optimize.enabled": true,
	})
	cut := time.Date(2030, 3, 5, 23, 45, 0, 0, parityZone) // Tuesday
	var pre []time.Time
	for at := parityStart; !at.After(time.Date(2030, 3, 5, 22, 0, 0, 0, parityZone)); at = at.Add(time.Hour) {
		pre = append(pre, at)
	}
	windowRun := time.Date(2030, 3, 5, 23, 30, 0, 0, parityZone)
	pre = append(pre, windowRun)
	sweepDue := time.Date(2030, 3, 6, 8, 0, 0, 0, parityZone)
	lastSweep := sweepDue.Add(-168 * time.Hour)
	// metadata_refresh is switched on with the cutover's restart, so HEAD's
	// rows hold no clock for it: the first poll seeds it and the first run
	// is one interval later, on both sides.
	enableAtCut := map[string]any{"scheduled.metadata_refresh.enabled": true, "scheduled.metadata_refresh.interval": 120}
	const unclocked tasks.Kind = "books.maintenance.metadata-refresh"
	steps := []parityStep{{at: cut, edit: enableAtCut}}
	for at := time.Date(2030, 3, 6, 0, 0, 0, 0, parityZone); !at.After(time.Date(2030, 3, 7, 1, 0, 0, 0, parityZone)); at = at.Add(time.Hour) {
		steps = append(steps, parityStep{at: at})
	}
	res := runParity(t, parityScenario{
		head: head, setup: paritySetup{dedup: true, activity: true, metadata: true}, steps: steps, pre: pre,
		settings: map[string]string{"library_scan_full_last_run": lastSweep.UTC().Format(time.RFC3339)},
	})
	for _, d := range res.diffs {
		t.Error(d)
	}

	// HEAD's history did what the scenario says.
	if got := res.settings["maintenance_window_last_run"]; got != "2030-03-05" {
		t.Fatalf("HEAD's window clock = %q, want its 23:30 run's local date 2030-03-05", got)
	}
	compactTick, err := time.Parse(time.RFC3339, res.settings["scheduled_task_last_tick.db_optimize"])
	if err != nil {
		t.Fatalf("HEAD's db_optimize clock: %v", err)
	}

	// The imported clocks.
	clock := func(k tasks.Kind) tasks.Clock {
		c, ok := res.imported[k]
		if !ok {
			t.Fatalf("M4 imported no clock for %s", k)
		}
		return c
	}
	if c := clock(tasks.WindowKind("nightly")); !c.LastDue.Equal(time.Date(2030, 3, 5, 0, 0, 0, 0, parityZone)) {
		t.Errorf("window clock LastDue = %s, want the run's local midnight 2030-03-05T00:00-05:00", c.LastDue)
	}
	if c := clock(kindrename.FullSweepKind); !c.LastDue.Equal(lastSweep) {
		t.Errorf("full sweep clock LastDue = %s, want last_run %s", c.LastDue, lastSweep)
	}
	for _, k := range []tasks.Kind{"core.storage.compact", "books.maintenance.aux-store-compact"} {
		if c := clock(k); !c.LastDue.Equal(compactTick) {
			t.Errorf("%s clock LastDue = %s, want db_optimize's tick %s", k, c.LastDue, compactTick)
		}
	}

	// First boot: only the on-start kinds.
	wantBoot := []string{
		"enqueue books.maintenance.purge-deleted on_start",
		"enqueue books.maintenance.temp-file-cleanup on_start",
		"enqueue books.maintenance.transcode-temp-cleanup on_start",
	}
	if !slices.Equal(res.core[0], wantBoot) || !slices.Equal(res.head[0], wantBoot) {
		t.Errorf("first boot at %s: core %v, HEAD %v, want only %v", cut.Format(time.RFC3339), res.core[0], res.head[0], wantBoot)
	}

	// Every imported interval clock: nothing before LastDue + every, then a
	// fire at the first step at or after it. A kind core ran at the boot
	// (difference (l)) is re-anchored there: its first scheduled fire is one
	// interval after the cutover's boot, and never before HEAD's due instant.
	if _, ok := res.imported[unclocked]; ok {
		t.Fatalf("M4 imported a clock for %s, which HEAD never ran", unclocked)
	}
	view := coreViewOf(t, withHead(t, head, enableAtCut))
	startedAtBoot := func(k tasks.Kind) bool {
		return slices.Contains(res.core[0], fmt.Sprintf("enqueue %s on_start", k))
	}
	firstFire := func(k tasks.Kind) (time.Time, bool) {
		for i, st := range steps {
			if slices.Contains(res.core[i], fmt.Sprintf("enqueue %s schedule", k)) {
				return st.at, true
			}
		}
		return time.Time{}, false
	}
	checked := map[tasks.Kind]time.Time{}
	for _, d := range scheduleDecls {
		c, ok := res.imported[d.kind]
		sc := view.schedules[scheduleKeyPrefix+d.key]
		if !sc.Enabled || sc.Every <= 0 {
			continue
		}
		if !ok && d.kind != unclocked {
			continue
		}
		every := time.Duration(sc.Every)
		// A kind with no clock on record is seeded by the boot's first poll
		// (the cutover instant): it runs one interval later, never at once.
		base := cut
		if ok && !startedAtBoot(d.kind) {
			base = c.LastDue
		}
		headDue := base.Add(every)
		if ok {
			headDue = c.LastDue.Add(every)
		}
		due := base.Add(every)
		want := time.Time{}
		for _, st := range steps {
			if !st.at.Before(due) {
				want = st.at
				break
			}
		}
		got, fired := firstFire(d.kind)
		switch {
		case fired && got.Before(headDue):
			t.Errorf("%s fired at %s, before HEAD's next due instant %s", d.kind, got, headDue)
		case want.IsZero() && fired:
			t.Errorf("%s fired at %s, before its next due instant %s", d.kind, got, due)
		case !want.IsZero() && (!fired || !got.Equal(want)):
			t.Errorf("%s: first scheduled fire %s (%v), want %s (from %s + %s)", d.kind, got, fired, want, base, every)
		}
		checked[d.kind] = want
	}
	for k, want := range map[tasks.Kind]time.Time{
		kindrename.FullSweepKind:              sweepDue,
		"books.maintenance.aux-store-compact": compactTick.Add(24 * time.Hour),
		unclocked:                             time.Date(2030, 3, 6, 2, 0, 0, 0, parityZone), // seeded 23:45, due 01:45
	} {
		if got, ok := checked[k]; !ok || !got.Equal(want) {
			t.Errorf("%s: due %s (checked %v), want %s", k, got, ok, want)
		}
	}

	// The window: not again that evening, the next one at 23:00.
	for i, st := range steps {
		opened := slices.ContainsFunc(res.core[i], func(l string) bool { return strings.HasPrefix(l, "window ") })
		if want := st.at.Equal(time.Date(2030, 3, 6, 23, 0, 0, 0, parityZone)); opened != want {
			t.Errorf("%s: window opened %v, want %v", st.at.Format(time.RFC3339), opened, want)
		}
	}
}
