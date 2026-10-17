// file: internal/scheduler/v3_schedule.go
// version: 1.0.0
// guid: b41c7e92-6d05-4a38-9f1e-2c8a5d70e3b6
// last-edited: 2026-10-17

package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	"github.com/falkcorp/audiobook-organizer/internal/opsmetrics"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// A definition declares its timer in exactly one place, ops.Schedule, and this
// file runs it on the clocks the scheduler already has. No new timer mechanism
// is introduced:
//
//   - Every   -> the durable interval clock (claimIntervalRun)
//   - DailyAt -> the daily clock (claimDailyAtRun)
//   - Cron    -> the evaluator in cron.go, with the same durable stamp
//   - InMaintenanceWindow -> a task in maintenanceOrder that the nightly
//     window op runs exactly as it runs a TaskDefinition
//
// All of them are keyed by the def ID (there is no second name), and the due
// checks of Every, DailyAt and Cron run on ONE goroutine for the whole
// scheduler (runV3Loop), not one per def.

// schedStore is the part of the v3 store this file calls.
type schedStore interface {
	GetSchedV3(defID string) (*database.SchedState, error)
	PutSchedV3(st database.SchedState) error
}

// ScheduleStatus is the API-facing view of one registered schedule (also used
// by the tasks page).
type ScheduleStatus struct {
	DefID string
	// Kind is "every", "daily_at", "cron", "window" or "disabled".
	Kind string
	// Active is false for "declared, not active": a cron nothing drives, or a
	// When gate that is currently off.
	Active bool
	// Live reports that the schedule says .Live().
	Live     bool
	LastFire *time.Time
	NextFire *time.Time
	Missed   int64
}

// v3Entry is one registered schedule. Guarded by v3Scheduler.mu.
type v3Entry struct {
	defID  string
	sched  *ops.Schedule // nil for a disabled adapted def
	kind   string
	cron   CronSpec
	hour   int
	minute int
	writer bool
	live   bool
	// external: a legacy TaskDefinition still enqueues this def, so this file
	// reports the schedule but does not fire it (no double run).
	external bool
	// disabled: an adapted def whose v2 cron nothing drives (F1, D24).
	disabled bool

	gate      bool // last observed When gate
	lastFire  *time.Time
	lastRunID string
	missed    int64
	lastLag   time.Duration
	coalesced int64
}

// v3Scheduler holds the registered schedules. The zero value is usable; the
// function fields are seams that default to the real registry.
type v3Scheduler struct {
	mu      sync.Mutex
	entries map[string]*v3Entry
	order   []string // def IDs, sorted

	now       func() time.Time
	loc       *time.Location
	enqueue   func(ctx context.Context, defID string, params json.RawMessage) (string, error)
	hasActive func(defID string) bool
	v2Cron    func(defID string) (string, bool)
	store     func() schedStore
}

func (v *v3Scheduler) clock() time.Time {
	if v.now != nil {
		return v.now()
	}
	return time.Now()
}

func (v *v3Scheduler) zone() *time.Location {
	if v.loc != nil {
		return v.loc
	}
	return time.Local
}

func (v *v3Scheduler) has(name string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	e, ok := v.entries[name]
	return ok && e.kind == "window" && !e.external
}

// drivenDefs is the set of def IDs a registered TaskDefinition enqueues.
func (ts *TaskScheduler) drivenDefs() map[string]bool {
	driven := map[string]bool{}
	for name := range ts.tasks {
		if id, ok := taskV2DefIDs[name]; ok {
			driven[id] = true
		}
	}
	return driven
}

func (ts *TaskScheduler) schedStore() schedStore {
	if ts.v3.store != nil {
		return ts.v3.store()
	}
	if ts.deps.Store == nil {
		return nil
	}
	st, ok := database.AsCapability[schedStore](ts.deps.Store())
	if !ok {
		return nil
	}
	return st
}

func (ts *TaskScheduler) v3Enqueue(ctx context.Context, defID string, params json.RawMessage) (string, error) {
	if ts.v3.enqueue != nil {
		return ts.v3.enqueue(ctx, defID, params)
	}
	if ts.deps.OpRegistry == nil {
		return "", errors.New("operation registry not initialized")
	}
	return ts.deps.OpRegistry.EnqueueOp(ctx, defID, params)
}

func (ts *TaskScheduler) v3HasActive(defID string) bool {
	if ts.v3.hasActive != nil {
		return ts.v3.hasActive(defID)
	}
	return ts.hasActiveV2Op(defID)
}

func (ts *TaskScheduler) v3V2Cron(defID string) (string, bool) {
	if ts.v3.v2Cron != nil {
		return ts.v3.v2Cron(defID)
	}
	if ts.deps.OpRegistry == nil {
		return "", false
	}
	d, ok := ts.deps.OpRegistry.Def(defID)
	if !ok || d.Schedule == nil {
		return "", false
	}
	return *d.Schedule, true
}

// RegisterSchedules turns every definition that declares an ops.Schedule into
// scheduler state, keyed by the def ID (there is no second name). It is called
// once at startup with the catalog's definitions, before Start. A definition
// without a Schedule is ignored. An adapted v2 definition keeps its
// TaskDefinition driver unchanged; one whose v2 cron nothing drives is
// recorded as "disabled" (declared, not active) and never fires. A native
// definition that a legacy TaskDefinition still enqueues is reported but not
// fired here, so a port never runs twice.
func (ts *TaskScheduler) RegisterSchedules(defs []ops.Definition) error {
	driven := ts.drivenDefs()
	var errs []error
	var windowIDs []string
	for _, d := range defs {
		c := ops.Compile(d)
		if c.Kind == ops.KindAdapted {
			ts.registerAdaptedSchedule(c.ID, driven)
			continue
		}
		s := c.Common.Schedule
		if s == nil {
			continue
		}
		if err := s.Err(); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", c.ID, err))
			continue
		}
		e := &v3Entry{defID: c.ID, sched: s, kind: s.TimerKind(), live: s.IsLive(),
			writer: !c.Common.Effects.IsReadOnly(), gate: s.Enabled(), external: driven[c.ID]}
		switch e.kind {
		case "daily_at":
			h, m, err := parseDailyAt(s.At())
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", c.ID, err))
				continue
			}
			e.hour, e.minute = h, m
		case "cron":
			spec, err := ParseCron(s.Expr())
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", c.ID, err))
				continue
			}
			e.cron = spec
		}
		if err := ts.addV3Entry(e); err != nil {
			errs = append(errs, err)
			continue
		}
		if e.external {
			continue
		}
		ts.loadSchedState(e)
		ts.seedFromLegacyClock(e)
		if e.kind == "window" {
			windowIDs = append(windowIDs, e.defID)
		}
	}
	ts.registerWindowTasks(windowIDs)
	return errors.Join(errs...)
}

func (ts *TaskScheduler) addV3Entry(e *v3Entry) error {
	ts.v3.mu.Lock()
	defer ts.v3.mu.Unlock()
	if ts.v3.entries == nil {
		ts.v3.entries = map[string]*v3Entry{}
	}
	if _, dup := ts.v3.entries[e.defID]; dup {
		return fmt.Errorf("schedule for %s registered twice", e.defID)
	}
	ts.v3.entries[e.defID] = e
	ts.v3.order = append(ts.v3.order, e.defID)
	sort.Strings(ts.v3.order)
	return nil
}

// registerAdaptedSchedule records an adapted v2 def that declares a cron. If a
// task enqueues it, the task keeps driving it; if nothing does, it is
// "disabled" and never fires (F1, D24): no TaskDefinition is invented for it.
func (ts *TaskScheduler) registerAdaptedSchedule(defID string, driven map[string]bool) {
	if _, ok := ts.v3V2Cron(defID); !ok {
		return
	}
	e := &v3Entry{defID: defID, kind: "cron", external: driven[defID], gate: true}
	if !e.external {
		e.kind, e.disabled, e.gate = "disabled", true, false
	}
	_ = ts.addV3Entry(e) // a duplicate adapted id cannot occur: the catalog holds one def per id
}

// loadSchedState restores last fire, missed count and last run id.
func (ts *TaskScheduler) loadSchedState(e *v3Entry) {
	st := ts.schedStore()
	if st == nil {
		return
	}
	rec, err := st.GetSchedV3(e.defID)
	if err != nil {
		schedLog.Warn("schedule state for %s unreadable: %v", e.defID, err)
		return
	}
	if rec == nil {
		return
	}
	ts.v3.mu.Lock()
	defer ts.v3.mu.Unlock()
	e.lastFire, e.lastRunID, e.missed = rec.LastFire, rec.LastRunID, rec.Missed
}

// seedFromLegacyClock keeps a ported op's cadence across the port. The old
// clock lives under the settings key of the task name; without copying it the
// first deploy after a port would see "never ran". It runs only when this def
// has no schedule record and no clock of its own.
func (ts *TaskScheduler) seedFromLegacyClock(e *v3Entry) {
	legacy := legacyTaskNameForDef(e.defID)
	if legacy == "" || e.kind == "window" {
		return
	}
	if st := ts.schedStore(); st != nil {
		if rec, err := st.GetSchedV3(e.defID); err == nil && rec != nil {
			return
		}
	}
	if _, ok := ts.loadIntervalLastTick(e.defID); ok {
		return
	}
	if t, ok := ts.loadIntervalLastTick(legacy); ok {
		ts.stampIntervalTick(e.defID, t)
		ts.v3.mu.Lock()
		e.lastFire = &t
		ts.v3.mu.Unlock()
	}
}

// registerWindowTasks adds each window-scheduled def to the maintenance
// window as a task named by its def ID, so the nightly window op runs it
// exactly as it runs a TaskDefinition. They are appended to maintenanceOrder
// in (order, def ID) order, after every task already there.
func (ts *TaskScheduler) registerWindowTasks(ids []string) {
	ts.v3.mu.Lock()
	slices.SortStableFunc(ids, func(a, b string) int {
		if d := ts.v3.entries[a].sched.Order() - ts.v3.entries[b].sched.Order(); d != 0 {
			return d
		}
		return cmpString(a, b)
	})
	ts.v3.mu.Unlock()
	for _, id := range ids {
		e := ts.v3.entries[id]
		ts.RegisterTask(TaskDefinition{
			Name:        id,
			Description: "Maintenance window task declared by ops.InMaintenanceWindow",
			Category:    "maintenance",
			TriggerFn: func(source string) (*database.Operation, error) {
				return ts.fireV3(e, time.Time{}, 0, source)
			},
			IsEnabled:              e.sched.Enabled,
			GetInterval:            func() time.Duration { return 0 },
			RunOnStart:             func() bool { return e.sched.RunsOnStart() && e.sched.Enabled() },
			RunInMaintenanceWindow: func() bool { return true },
		})
		ts.maintenanceOrder = append(ts.maintenanceOrder, id)
	}
}

func cmpString(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// runParams builds the params of a scheduled run. The mode comes from the
// schedule, never from a default (D8): a writer runs Live only when the
// schedule said .Live(), otherwise as a Preview.
func (e *v3Entry) runParams() (json.RawMessage, error) {
	base := e.sched.RunParams()
	if !e.writer {
		if len(base) == 0 {
			return json.RawMessage("{}"), nil
		}
		return base, nil
	}
	m := map[string]json.RawMessage{}
	if len(base) > 0 {
		if err := json.Unmarshal(base, &m); err != nil {
			return nil, fmt.Errorf("%s: schedule params are not an object: %w", e.defID, err)
		}
	}
	delete(m, "dryRun")
	if e.live {
		m["dry_run"] = json.RawMessage("false")
	} else {
		m["dry_run"] = json.RawMessage("true")
	}
	return json.Marshal(m)
}

// fireV3 enqueues one scheduled run. dueAt is when it was due (zero when the
// trigger has no due time, as for a window or start run); missed is how many
// due times were skipped before it. A run that finds the def's previous run
// still queued or running coalesces into it. At most one catch-up run is ever
// enqueued, however many fires were missed.
func (ts *TaskScheduler) fireV3(e *v3Entry, dueAt time.Time, missed int64, source string) (*database.Operation, error) {
	now := ts.v3.clock()
	ts.v3.mu.Lock()
	if missed > 0 {
		e.missed += missed
	}
	if !dueAt.IsZero() {
		e.lastLag = max(0, now.Sub(dueAt))
	}
	lag := e.lastLag
	ts.v3.mu.Unlock()
	if missed > 0 {
		opsmetrics.Default().ScheduleMissed(e.defID, missed)
	}
	if !dueAt.IsZero() {
		opsmetrics.Default().ScheduleLag(e.defID, lag)
	}

	if ts.v3HasActive(e.defID) {
		ts.v3.mu.Lock()
		e.coalesced++
		ts.v3.mu.Unlock()
		schedLog.Info("Scheduled run of %s coalesced into its running or queued run (source=%s)", e.defID, source)
		ts.persistSched(e)
		return nil, nil
	}
	params, err := e.runParams()
	if err != nil {
		return nil, err
	}
	id, err := ts.v3Enqueue(operations.WithTriggerSource(ts.lifecycleContext(), source), e.defID, params)
	if err != nil {
		return nil, fmt.Errorf("enqueue %s: %w", e.defID, err)
	}
	ts.v3.mu.Lock()
	e.lastFire = &now
	if id != "" {
		e.lastRunID = id
	}
	ts.v3.mu.Unlock()
	ts.persistSched(e)
	schedLog.Info("Scheduled %s started: op=%s live=%t lag=%s missed=%d", e.defID, id, e.live, lag, missed)
	return v2ScheduledOp(id, e.defID), nil
}

// persistSched writes the schedule record: after every fire and every gate edge.
func (ts *TaskScheduler) persistSched(e *v3Entry) {
	st := ts.schedStore()
	if st == nil {
		return
	}
	next, _ := ts.nextFireOf(e)
	ts.v3.mu.Lock()
	rec := database.SchedState{DefID: e.defID, LastFire: e.lastFire, LastRunID: e.lastRunID, Missed: e.missed}
	if !next.IsZero() {
		rec.NextFire = &next
	}
	ts.v3.mu.Unlock()
	if err := st.PutSchedV3(rec); err != nil {
		schedLog.Warn("schedule state for %s not saved: %v", e.defID, err)
	}
}

// claimCronRun is one due check of a Cron schedule. Like the interval and
// daily clocks, the first observation seeds the stamp and declines, and the
// stamp is written before the run is enqueued.
func (ts *TaskScheduler) claimCronRun(e *v3Entry, now time.Time) (due bool, dueAt time.Time, missed int64) {
	loc := ts.v3.zone()
	last, ok := ts.loadIntervalLastTick(e.defID)
	if !ok {
		ts.stampIntervalTick(e.defID, now)
		return false, time.Time{}, 0
	}
	dueAt, found := e.cron.Next(last, loc)
	if !found || now.Before(dueAt) {
		return false, time.Time{}, 0
	}
	n := int64(1)
	for t := dueAt; n < 1000; n++ {
		nt, more := e.cron.Next(t, loc)
		if !more || nt.After(now) {
			break
		}
		t = nt
	}
	ts.stampIntervalTick(e.defID, now)
	return true, dueAt, n - 1
}

// pollV3 is one due check of every Every, DailyAt and Cron schedule.
func (ts *TaskScheduler) pollV3(now time.Time) {
	ts.v3.mu.Lock()
	ids := slices.Clone(ts.v3.order)
	ts.v3.mu.Unlock()
	for _, id := range ids {
		ts.v3.mu.Lock()
		e := ts.v3.entries[id]
		skip := e.external || e.disabled || e.kind == "window"
		ts.v3.mu.Unlock()
		if skip {
			continue
		}
		ts.pollV3Entry(e, now)
	}
}

func (ts *TaskScheduler) pollV3Entry(e *v3Entry, now time.Time) {
	gate := e.sched.Enabled()
	ts.v3.mu.Lock()
	edge := gate != e.gate
	e.gate = gate
	ts.v3.mu.Unlock()
	if edge {
		ts.persistSched(e)
	}
	if !gate {
		return // not active right now; the clock is not advanced
	}
	var due bool
	var dueAt time.Time
	var missed int64
	switch e.kind {
	case "every":
		interval := e.sched.Interval()
		last, ok := ts.loadIntervalLastTick(e.defID)
		if due = ts.claimIntervalRun(e.defID, interval, now); due && ok {
			dueAt = last.Add(interval)
			missed = max(0, int64(now.Sub(last)/interval)-1)
		}
	case "daily_at":
		loc := ts.v3.zone()
		last, ok := ts.loadIntervalLastTick(e.defID)
		if due = ts.claimDailyAtRun(e.defID, e.hour, e.minute, loc, now); due && ok {
			dueAt = nextDailyAt(last, e.hour, e.minute, loc)
			n := int64(0)
			for t := dueAt; !t.After(now) && n < 1000; n++ {
				t = nextDailyAt(t, e.hour, e.minute, loc)
			}
			missed = max(0, n-1)
		}
	case "cron":
		due, dueAt, missed = ts.claimCronRun(e, now)
	}
	if !due {
		return
	}
	if _, err := ts.fireV3(e, dueAt, missed, operations.TriggerScheduled); err != nil {
		schedLog.Warn("Scheduled run of %s failed: %v", e.defID, err)
	}
}

// startV3 launches the one goroutine that drives every non-window schedule,
// and fires the OnStart ones once.
func (ts *TaskScheduler) startV3(shutdown chan struct{}, wg *sync.WaitGroup) {
	poll := intervalPollInterval
	n := 0
	ts.v3.mu.Lock()
	for _, id := range ts.v3.order {
		e := ts.v3.entries[id]
		if e.external || e.disabled || e.kind == "window" {
			continue
		}
		n++
		if e.kind == "every" {
			poll = min(poll, intervalPollFor(e.sched.Interval()))
		}
	}
	ts.v3.mu.Unlock()
	if n == 0 {
		return
	}
	wg.Go(func() {
		ts.runV3OnStart()
		ticker := time.NewTicker(poll)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				ts.pollV3(ts.v3.clock())
			case <-shutdown:
				return
			}
		}
	})
	schedLog.Info("Scheduled %d ops.Schedule definitions on one loop: poll=%s", n, poll)
}

// runV3OnStart fires each OnStart schedule once and stamps its clock, so the
// first poll does not fire it a second time.
func (ts *TaskScheduler) runV3OnStart() {
	ts.v3.mu.Lock()
	ids := slices.Clone(ts.v3.order)
	ts.v3.mu.Unlock()
	for _, id := range ids {
		ts.v3.mu.Lock()
		e := ts.v3.entries[id]
		skip := e.external || e.disabled || e.kind == "window" || !e.sched.RunsOnStart()
		ts.v3.mu.Unlock()
		if skip || !e.sched.Enabled() {
			continue
		}
		ts.stampIntervalTick(e.defID, ts.v3.clock())
		if _, err := ts.fireV3(e, time.Time{}, 0, operations.TriggerScheduled); err != nil {
			schedLog.Warn("Start run of %s failed: %v", e.defID, err)
		}
	}
}

// nextFireOf computes the next fire time of an entry, or false if it is
// manual, disabled, or its gate is off.
func (ts *TaskScheduler) nextFireOf(e *v3Entry) (time.Time, bool) {
	ts.v3.mu.Lock()
	external, disabled, kind := e.external, e.disabled, e.kind
	ts.v3.mu.Unlock()
	if disabled || kind == "window" || kind == "" || (e.sched != nil && !e.sched.Enabled()) {
		return time.Time{}, false
	}
	now := ts.v3.clock()
	last, ok := ts.loadIntervalLastTick(e.defID)
	if !ok {
		last = now
	}
	loc := ts.v3.zone()
	switch kind {
	case "every":
		return last.Add(e.sched.Interval()), !external
	case "daily_at":
		return nextDailyAt(last, e.hour, e.minute, loc), !external
	case "cron":
		if e.sched == nil {
			return time.Time{}, false
		}
		t, found := e.cron.Next(last, loc)
		return t, found && !external
	}
	return time.Time{}, false
}

// NextFire reports when a registered definition will next fire (for the def
// list), or false if it is manual, disabled, driven by a legacy task, or its
// config gate (Schedule.When) is currently off.
func (ts *TaskScheduler) NextFire(defID string) (time.Time, bool) {
	ts.v3.mu.Lock()
	e, ok := ts.v3.entries[defID]
	ts.v3.mu.Unlock()
	if !ok {
		return time.Time{}, false
	}
	return ts.nextFireOf(e)
}

// ScheduleStatuses lists every registered schedule, ordered by def ID.
func (ts *TaskScheduler) ScheduleStatuses() []ScheduleStatus {
	ts.v3.mu.Lock()
	ids := slices.Clone(ts.v3.order)
	ts.v3.mu.Unlock()
	out := make([]ScheduleStatus, 0, len(ids))
	for _, id := range ids {
		ts.v3.mu.Lock()
		e := ts.v3.entries[id]
		ts.v3.mu.Unlock()
		next, hasNext := ts.nextFireOf(e)
		ts.v3.mu.Lock()
		st := ScheduleStatus{DefID: id, Kind: e.kind, Active: !e.disabled && (e.sched == nil || e.sched.Enabled()),
			Live: e.live, LastFire: e.lastFire, Missed: e.missed}
		ts.v3.mu.Unlock()
		if hasNext {
			st.NextFire = &next
		}
		out = append(out, st)
	}
	return out
}
