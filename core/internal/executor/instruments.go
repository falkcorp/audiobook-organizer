// file: core/internal/executor/instruments.go
// version: 1.0.0
// guid: 0ead5429-af5c-47ad-afaa-91de7e8f5069
// last-edited: 2026-11-10

package executor

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/tasks"
)

// Attribute keys of the ops instruments; the core ops scope must declare
// them (Provider.Core("ops", Attributes()...)).
const (
	attrDefID   = attribute.Key("def_id")
	attrOutcome = attribute.Key("outcome")
	attrModule  = attribute.Key("module")
	attrPrio    = attribute.Key("priority")
	attrReason  = attribute.Key("reason")
	attrLane    = attribute.Key("lane")
	attrFamily  = attribute.Key("family")
)

// Attributes are the attribute keys the executor's instruments use, for the
// scope the kernel builds (Provider.Core("ops", executor.Attributes()...)).
// Name coined by F2-03.
func Attributes() []attribute.Key {
	return []attribute.Key{attrDefID, attrOutcome, attrModule, attrPrio, attrReason, attrLane, attrFamily}
}

// Bucket lists: HEAD's for the two histograms that exist at HEAD
// (internal/telemetry/views.go's run.duration and schedule_lag lists), so
// the `le` values do not change; ref_lock_wait is new and takes a
// lock-wait shaped list (milliseconds to ten minutes).
func runDurationBuckets() []float64 {
	return []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600, 1800, 3600, 7200, 14400, 43200, 86400}
}

func scheduleLagBuckets() []float64 { return []float64{1, 5, 15, 60, 300, 900, 3600, 21600, 86400} }

func refLockWaitBuckets() []float64 {
	return []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5, 10, 60, 600}
}

// Instrument names under the scope prefix (<ns>.ops): HEAD's names for the
// HEAD families, 5T.11's for the new ones. 18 in all.
const (
	insRuns         = "runs"
	insRunDuration  = "run.duration"
	insItems        = "items"
	insInflight     = "inflight"
	insProgressAge  = "progress_age"
	insProgressTO   = "progress_timeout"
	insZombies      = "zombies"
	insScheduleLag  = "schedule_lag"
	insScheduleMiss = "schedule_missed"
	insQueueDepth   = "queue_depth"
	insDeferred     = "dispatch_deferred"
	insCPUSlots     = "cpu_slots_in_use"
	insLaneInUse    = "lane_in_use"
	insRefLockWait  = "ref_lock_wait"
	insHeldRuns     = "held_runs"
	insYields       = "yields"
	insScheduleErrs = "schedule_errors"
	insUndecodable  = "undecodable_records"
)

// InstrumentNames are the 18 instrument names under the scope prefix, for
// the metrics tests (05-metrics §12's executor cell). Name coined by F2-03.
func InstrumentNames() []string {
	return []string{insRuns, insRunDuration, insItems, insInflight, insProgressAge, insProgressTO, insZombies,
		insScheduleLag, insScheduleMiss, insQueueDepth, insDeferred, insCPUSlots, insLaneInUse, insRefLockWait,
		insHeldRuns, insYields, insScheduleErrs, insUndecodable}
}

// instruments are the task-run instruments of 5T.11. A nil *instruments
// (InstrumentsOff) records nothing.
type instruments struct {
	e                                                         *executor
	runs, items, schedMiss, deferred, yields, schedErr, undec metric.Int64Counter
	duration, schedLag, refWait                               metric.Float64Histogram
	queue                                                     map[[2]string]int64 // module, priority -> queued, last pass
}

func newInstruments(sc *metrics.Scope, e *executor) (*instruments, error) {
	m, prefix := sc.Meter(), sc.Prefix()
	name := func(n string) string { return prefix + "." + n }
	for n, b := range map[string][]float64{insRunDuration: runDurationBuckets(), insScheduleLag: scheduleLagBuckets(), insRefLockWait: refLockWaitBuckets()} {
		if err := sc.View(name(n), b...); err != nil {
			return nil, fmt.Errorf("executor: view %s: %w", n, err)
		}
	}
	in := &instruments{e: e}
	var err error
	counter := func(n, unit, desc string) metric.Int64Counter {
		if err != nil {
			return nil
		}
		var c metric.Int64Counter
		c, err = m.Int64Counter(name(n), metric.WithUnit(unit), metric.WithDescription(desc))
		return c
	}
	hist := func(n, desc string) metric.Float64Histogram {
		if err != nil {
			return nil
		}
		var h metric.Float64Histogram
		h, err = m.Float64Histogram(name(n), metric.WithUnit("s"), metric.WithDescription(desc))
		return h
	}
	in.runs = counter(insRuns, "{run}", "Task runs by def_id and outcome (started, completed, failed, canceled, timed_out, dropped).")
	in.items = counter(insItems, "{item}", "Items handled by task runs, by def_id and outcome.")
	in.schedMiss = counter(insScheduleMiss, "{run}", "Scheduled fires folded into one catch-up run, by def_id.")
	in.deferred = counter(insDeferred, "{run}", "Queued runs held by a dispatch gate, by reason (counted once per run and reason).")
	in.yields = counter(insYields, "{yield}", "Runs that yielded and continued in place, by def_id.")
	in.schedErr = counter(insScheduleErrs, "{error}", "Schedule clock read errors that skipped a poll, by def_id.")
	in.undec = counter(insUndecodable, "{record}", "Stored task records that could not be decoded and were hidden, by family.")
	in.duration = hist(insRunDuration, "Wall-clock duration of a finished task run, by def_id and outcome.")
	in.schedLag = hist(insScheduleLag, "Seconds between a scheduled run's due time and its enqueue, by def_id.")
	in.refWait = hist(insRefLockWait, "Seconds a run waited for its ref locks, by def_id.")
	if err != nil {
		return nil, fmt.Errorf("executor: instrument: %w", err)
	}
	obs := func(n, unit, desc string) metric.Int64ObservableGauge {
		if err != nil {
			return nil
		}
		var g metric.Int64ObservableGauge
		g, err = m.Int64ObservableGauge(name(n), metric.WithUnit(unit), metric.WithDescription(desc))
		return g
	}
	obsF := func(n, desc string) metric.Float64ObservableGauge {
		if err != nil {
			return nil
		}
		var g metric.Float64ObservableGauge
		g, err = m.Float64ObservableGauge(name(n), metric.WithUnit("s"), metric.WithDescription(desc))
		return g
	}
	inflight := obs(insInflight, "{run}", "Task runs holding a worker, by def_id and module.")
	progAge := obsF(insProgressAge, "Seconds since the most idle executing run of a def_id last reported progress, by def_id and module.")
	progTO := obsF(insProgressTO, "The watchdog's progress timeout of a def_id with a run in flight, by def_id and module.")
	zombies := obs(insZombies, "{run}", "Runs whose goroutine outlived its cancel grace and has not returned; they hold their key and write set until it does.")
	queue := obs(insQueueDepth, "{run}", "Queued runs by module and priority, as of the last dispatch pass.")
	cpu := obs(insCPUSlots, "{slot}", "core.tasks.cpu_slots slots held by CPU() items.")
	lanes := obs(insLaneInUse, "{lease}", "Lane leases held, by lane.")
	held := obs(insHeldRuns, "{run}", "Unfinished runs of disabled modules (held), by module, as of boot.")
	if err != nil {
		return nil, fmt.Errorf("executor: instrument: %w", err)
	}
	_, err = m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		in.observe(o, inflight, progAge, progTO, zombies, queue, cpu, lanes, held)
		return nil
	}, inflight, progAge, progTO, zombies, queue, cpu, lanes, held)
	if err != nil {
		return nil, fmt.Errorf("executor: instrument callback: %w", err)
	}
	return in, nil
}

func set(kvs ...attribute.KeyValue) metric.MeasurementOption {
	return metric.WithAttributeSet(attribute.NewSet(kvs...))
}

// observe is the one collection callback of the observable gauges.
func (in *instruments) observe(o metric.Observer, inflight metric.Int64ObservableGauge, progAge, progTO metric.Float64ObservableGauge,
	zombies, queue, cpu, lanes, held metric.Int64ObservableGauge) {
	e := in.e
	now := e.now()
	type perKind struct {
		n   int64
		age time.Duration
		to  time.Duration
		mod string
	}
	kinds := map[tasks.Kind]*perKind{}
	e.mu.Lock()
	var z int64
	for _, a := range e.running {
		if a.zombie {
			continue
		}
		k := kinds[a.d.kind]
		if k == nil {
			k = &perKind{mod: a.d.module, to: a.d.progTO}
			kinds[a.d.kind] = k
		}
		k.n++
		if a.live {
			k.age = max(k.age, now.Sub(time.Unix(0, a.lastReport.Load())))
		}
	}
	for _, n := range e.zombies {
		z += int64(n)
	}
	q := make(map[[2]string]int64, len(in.queue))
	for k, v := range in.queue {
		q[k] = v
	}
	heldBy := map[string]int64{}
	for m, n := range e.heldCount {
		heldBy[m] = n
	}
	e.mu.Unlock()
	for kind, k := range kinds {
		attrs := set(attrDefID.String(string(kind)), attrModule.String(k.mod))
		o.ObserveInt64(inflight, k.n, attrs)
		o.ObserveFloat64(progAge, k.age.Seconds(), attrs)
		o.ObserveFloat64(progTO, k.to.Seconds(), attrs)
	}
	o.ObserveInt64(zombies, z)
	for k, v := range q {
		o.ObserveInt64(queue, v, set(attrModule.String(k[0]), attrPrio.String(k[1])))
	}
	o.ObserveInt64(cpu, int64(e.cpu.used()))
	for id, ln := range e.lanes {
		o.ObserveInt64(lanes, int64(ln.used()), set(attrLane.String(string(id))))
	}
	for m, n := range heldBy {
		o.ObserveInt64(held, n, set(attrModule.String(m)))
	}
}

func (in *instruments) runStarted(a *attempt) {
	if in == nil {
		return
	}
	in.runs.Add(context.Background(), 1, set(attrDefID.String(string(a.d.kind)), attrOutcome.String("started")))
}

// runEnded records a terminal transition.
func (in *instruments) runEnded(r tasks.RunRecord) {
	if in == nil {
		return
	}
	outcome := map[tasks.RunState]string{
		tasks.StateSucceeded: "completed", tasks.StateFailed: "failed", tasks.StateCanceled: "canceled",
		tasks.StateTimedOut: "timed_out", tasks.StateInterruptedDropped: "dropped",
	}[r.State]
	attrs := set(attrDefID.String(string(r.Kind)), attrOutcome.String(outcome))
	ctx := context.Background()
	in.runs.Add(ctx, 1, attrs)
	if !r.Started.IsZero() && !r.Finished.IsZero() {
		in.duration.Record(ctx, r.Finished.Sub(r.Started).Seconds(), attrs)
	}
	if r.Done > 0 {
		in.items.Add(ctx, int64(r.Done), set(attrDefID.String(string(r.Kind)), attrOutcome.String("processed")))
	}
}

func (in *instruments) yielded(kind tasks.Kind) {
	if in == nil {
		return
	}
	in.yields.Add(context.Background(), 1, set(attrDefID.String(string(kind))))
}

func (in *instruments) deferredRun(reason string) {
	if in == nil {
		return
	}
	in.deferred.Add(context.Background(), 1, set(attrReason.String(reason)))
}

func (in *instruments) refLockWait(kind tasks.Kind, d time.Duration) {
	if in == nil {
		return
	}
	in.refWait.Record(context.Background(), d.Seconds(), set(attrDefID.String(string(kind))))
}

func (in *instruments) undecodable(family string) {
	if in == nil {
		return
	}
	in.undec.Add(context.Background(), 1, set(attrFamily.String(family)))
}

// queueDepth replaces the queue-depth sample with one pass's counts.
func (in *instruments) queueDepth(c map[[2]string]int64) {
	if in == nil {
		return
	}
	in.e.mu.Lock()
	in.queue = c
	in.e.mu.Unlock()
}

// ScheduleMetrics records the scheduler families, which this executor owns
// (5T.11: "the executor owns the run families and core's scheduler both
// schedule families"); core's scheduler (F2-04) records through it. Name
// coined by F2-03.
type ScheduleMetrics interface {
	ScheduleLag(kind tasks.Kind, lag time.Duration)
	ScheduleMissed(kind tasks.Kind, n int64)
	ScheduleError(kind tasks.Kind)
}

// Schedules returns x's ScheduleMetrics (a no-op when its instruments are
// off, or x is not this package's executor).
func Schedules(x tasks.Executor) ScheduleMetrics {
	if e, ok := x.(*executor); ok && e.ins != nil {
		return e.ins
	}
	return (*instruments)(nil)
}

func (in *instruments) ScheduleLag(kind tasks.Kind, lag time.Duration) {
	if in == nil {
		return
	}
	in.schedLag.Record(context.Background(), lag.Seconds(), set(attrDefID.String(string(kind))))
}

func (in *instruments) ScheduleMissed(kind tasks.Kind, n int64) {
	if in == nil {
		return
	}
	in.schedMiss.Add(context.Background(), n, set(attrDefID.String(string(kind))))
}

func (in *instruments) ScheduleError(kind tasks.Kind) {
	if in == nil {
		return
	}
	in.schedErr.Add(context.Background(), 1, set(attrDefID.String(string(kind))))
}

// prioName is a priority's label value.
func prioName(p tasks.Priority) string {
	switch p {
	case tasks.PriorityLow:
		return "low"
	case tasks.PriorityNormal:
		return "normal"
	case tasks.PriorityHigh:
		return "high"
	case tasks.PrioritySystem:
		return "system"
	default:
		return strconv.Itoa(int(p))
	}
}
