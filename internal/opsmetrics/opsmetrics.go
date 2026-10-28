// file: internal/opsmetrics/opsmetrics.go
// version: 1.5.1
// guid: 0b6e9c35-18d4-4f7a-b2c1-6a93e5d07f48
// last-edited: 2026-10-28

// Package opsmetrics holds the OTel instruments for operation runs (spec 11
// §3.5 and §3.7). One meter, telemetry.Meter("operations"); every instrument
// carries def_id (a closed set, see defids.go) and never a per-run id.
//
// Wired by the v2 registry (internal/operations/registry):
//
//	audiobook_organizer.ops.runs          counter    {def_id, outcome}   -> audiobook_organizer_ops_runs_total
//	audiobook_organizer.ops.run.duration  histogram  {def_id, outcome}   -> audiobook_organizer_ops_run_duration_seconds
//	audiobook_organizer.ops.items         counter    {def_id, outcome}   -> audiobook_organizer_ops_items_total
//	audiobook_organizer.ops.inflight      obs. gauge {def_id, plugin}    -> audiobook_organizer_ops_inflight
//	audiobook_organizer.ops.progress_age  obs. gauge {def_id, plugin}    -> audiobook_organizer_ops_progress_age_seconds
//	audiobook_organizer.ops.progress_timeout obs. gauge {def_id, plugin} -> audiobook_organizer_ops_progress_timeout_seconds
//	audiobook_organizer.ops.zombies       obs. gauge (no attributes)  -> audiobook_organizer_ops_zombies
//	audiobook_organizer.ops.abandoned_calls obs. gauge {source}       -> audiobook_organizer_ops_abandoned_calls
//
// ops.inflight counts runs holding a worker slot. That includes a run the
// dispatcher has claimed (its stub handle is in the registry's running map)
// but no worker has picked up yet, so it is "dispatched", not "executing".
// ops.progress_age is the liveness signal: seconds since the most idle
// executing run of the key last called UpdateProgress (a same-value heartbeat
// counts), or since its attempt started when it has not reported yet. Runs not
// yet picked up by a worker are left out of it. ops.progress_timeout is the
// silence budget the registry watchdog allows that def: its effective
// ProgressTimeout (the watchdog's default when the def leaves it 0). The stall
// alert compares the two, so a def that declares a long budget (a LivenessNone
// def allowed 2h of silence) does not page inside it. All three gauges come
// from one collection callback (RegisterInflight).
//
// ops.zombies is the number of runs whose goroutine outlived the abandon
// grace and has not returned (the registry's ZombieCount), summed over every
// source registered with RegisterZombies.
//
// ops.abandoned_calls is the number of uncancellable calls (a stat on a stuck
// mount, a tag parse) that boundedcall.Call gave up on and whose goroutine has
// not returned, one point per source name registered with RegisterAbandoned.
// A value that stays above zero is a dead mount holding OS threads.
//
// runs outcomes: started, completed, failed, canceled, timed_out, dropped.
// started is counted when an attempt begins executing (Started); the other
// five are run ends (Finished, or Ended for an end with no timed attempt).
// timed_out is a run stopped by its def's Timeout (a v2 "canceled" row whose
// message starts with state.TimeoutPrefix); dropped is a run whose row became
// interrupted_dropped. The interrupted pauses (interrupted_quiesced,
// interrupted_ask) are not an outcome: the resumed attempt reports its own.
// Run ends are not paired with started: a run dropped before any attempt of
// it executed (the boot sweep dropping a queued row, a force-drop at pickup, a
// ResumeDrop scan quiesced before it started) has a dropped end and no
// started, and a resumed run has one started per attempt and one end. Compare
// rates of each outcome, not started against the sum of ends.
//
// items outcomes: processed, ok, failed, skipped. The v2 registry can only
// count progress, so it records processed; the v3 runner (05-PR5) records ok,
// failed and skipped. NOT the legacy gauge audiobook_organizer_op_items_total
// (no "s" in "ops"; per-run op_id label, emitted by internal/metrics).
//
// Declared but not wired here: see Unwired. They have no recorder yet because
// an OTel instrument created without a golden row and a seed fails the series
// contract test (internal/telemetry/contract); 05-PR5 adds each with its row
// (05-PR2 added ops.zombies that way).
package opsmetrics

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
	"github.com/falkcorp/audiobook-organizer/internal/telemetry"
)

// Run outcome values of ops.runs and ops.run.duration.
const (
	OutcomeStarted   = "started"
	OutcomeCompleted = "completed"
	OutcomeFailed    = "failed"
	OutcomeCanceled  = "canceled"
	OutcomeTimedOut  = "timed_out"
	OutcomeDropped   = "dropped"
)

// Item outcome values of ops.items.
const (
	ItemsProcessed = "processed"
	ItemsOK        = "ok"
	ItemsFailed    = "failed"
	ItemsSkipped   = "skipped"
)

// EmittedKeys are the attribute keys this package can put on a point. A test
// pins that op_id is not among them.
var EmittedKeys = []attribute.Key{telemetry.DefID, telemetry.Outcome, telemetry.Plugin}

// UnwiredInstrument names a declared instrument nothing records yet.
type UnwiredInstrument struct {
	Name  string // OTel name
	Kind  string // counter, histogram, gauge
	Unit  string
	Owner string // the task that wires it
}

// Unwired lists the spec §3.7 instruments this package does not create yet.
var Unwired = []UnwiredInstrument{
	{"audiobook_organizer.ops.queue_depth", "gauge", "{run}", "05-PR5"},
	{"audiobook_organizer.ops.fenced_writes", "counter", "{write}", "05-PR5"},
	{"audiobook_organizer.ops.checkpoint_age", "gauge", "s", "05-PR5"},
}

// OutcomeFor maps a v2 run status and its error message to the run-end
// outcome recorded for it, with the reading state.FromV2 gives the v3
// projection: a "canceled" row whose message starts with state.TimeoutPrefix
// timed out, and interrupted_dropped is dropped. ok is false for every status
// that records nothing: interrupted_quiesced and interrupted_ask are a pause,
// not an end (the resumed attempt reports its own outcome), and non-terminal
// statuses are not outcomes. "timeout" is not a status the registry writes.
//
// Two places where it differs from FromV2 on purpose, both requeues: an
// interrupted_dropped row whose message is state.RequeuedMessage, and the
// legacy interrupted_restart spelling, are v3 dropped (with a successor) to
// FromV2 but no outcome here. A requeue is not a run end: the work continues
// under the successor, whose own end is counted.
func OutcomeFor(status, errMsg string) (outcome string, ok bool) {
	switch status {
	case string(state.Completed):
		return OutcomeCompleted, true
	case string(state.Failed):
		return OutcomeFailed, true
	case string(state.Canceled):
		if strings.HasPrefix(errMsg, state.TimeoutPrefix) {
			return OutcomeTimedOut, true
		}
		return OutcomeCanceled, true
	case string(state.InterruptedDropped):
		if errMsg == state.RequeuedMessage {
			return "", false
		}
		return OutcomeDropped, true
	}
	return "", false
}

// IsRunEnd reports whether outcome is one of the five run-end outcomes
// Finished and Ended record (started is counted by Started).
func IsRunEnd(outcome string) bool {
	switch outcome {
	case OutcomeCompleted, OutcomeFailed, OutcomeCanceled, OutcomeTimedOut, OutcomeDropped:
		return true
	}
	return false
}

// InflightKey groups in-flight runs.
type InflightKey struct{ DefID, Plugin string }

// InflightStat is one key's in-flight state at collection time.
type InflightStat struct {
	// Runs is the number of runs holding a worker slot, including runs
	// dispatched but not yet picked up by a worker.
	Runs int64
	// ProgressAge is how long the key's most idle executing run has gone
	// without a progress report (since its attempt started when it has not
	// reported yet). Zero when none of the key's runs is executing.
	ProgressAge time.Duration
	// ProgressTimeout is the def's effective watchdog budget: how long one of
	// its runs may go without a progress report before the watchdog cancels
	// it. Zero when the key has no run.
	ProgressTimeout time.Duration
}

// Recorder holds the instruments of one meter provider.
type Recorder struct {
	meter     metric.Meter
	runs      metric.Int64Counter
	duration  metric.Float64Histogram
	items     metric.Int64Counter
	inflight  metric.Int64ObservableGauge
	progAge   metric.Float64ObservableGauge
	progTO    metric.Float64ObservableGauge
	zombies   metric.Int64ObservableGauge
	abandoned metric.Int64ObservableGauge
	schedLag  metric.Float64Histogram
	schedMiss metric.Int64Counter

	// zombieMu guards zombieSrc, the RegisterZombies sources the one
	// ops.zombies callback sums. The callback copies them out and releases
	// zombieMu before calling any (a source takes its registry's lock).
	zombieMu   sync.Mutex
	zombieSrc  map[uint64]func() int64
	zombieNext uint64

	// abandonedMu guards abandonedSrc, the RegisterAbandoned counters the one
	// ops.abandoned_calls callback reads.
	abandonedMu   sync.Mutex
	abandonedSrc  map[uint64]abandonedSource
	abandonedNext uint64
}

// abandonedSource is one counter behind ops.abandoned_calls.
type abandonedSource struct {
	name  string
	count *atomic.Int64
}

// New creates the instruments on mp. The metric API returns a no-op instrument
// alongside any error, so a failure degrades to missing series.
func New(mp metric.MeterProvider) *Recorder {
	m := telemetry.MeterFrom(mp, "operations")
	r := &Recorder{meter: m}
	r.runs, _ = m.Int64Counter("audiobook_organizer.ops.runs",
		metric.WithUnit("{run}"),
		metric.WithDescription("Operation runs by def_id and outcome (started, completed, failed, canceled, timed_out, dropped)."))
	r.duration, _ = m.Float64Histogram("audiobook_organizer.ops.run.duration",
		metric.WithUnit("s"),
		metric.WithDescription("Wall-clock duration of a finished operation run attempt, by def_id and outcome."))
	r.items, _ = m.Int64Counter("audiobook_organizer.ops.items",
		metric.WithUnit("{item}"),
		metric.WithDescription("Items handled by operation runs, by def_id and outcome. Not the legacy gauge op_items_total."))
	r.inflight, _ = m.Int64ObservableGauge("audiobook_organizer.ops.inflight",
		metric.WithUnit("{run}"),
		metric.WithDescription("Operation runs currently holding a worker slot, by def_id and plugin. Includes runs dispatched but not yet picked up by a worker, so it is not a count of executing runs."))
	r.progAge, _ = m.Float64ObservableGauge("audiobook_organizer.ops.progress_age",
		metric.WithUnit("s"),
		metric.WithDescription("Seconds since the most idle executing run of a def_id last reported progress (or started its attempt, if it has not reported yet); 0 when none is executing."))
	r.progTO, _ = m.Float64ObservableGauge("audiobook_organizer.ops.progress_timeout",
		metric.WithUnit("s"),
		metric.WithDescription("Effective watchdog ProgressTimeout in seconds of a def_id with a run in flight (the default when the def sets none); the silence budget ops.progress_age is judged against. 0 when none is in flight."))
	r.zombies, _ = m.Int64ObservableGauge("audiobook_organizer.ops.zombies",
		metric.WithUnit("{run}"),
		metric.WithDescription("Operation runs whose goroutine outlived the abandon grace after being stopped and has not returned yet. They hold their concurrency key and write set until it does."))
	r.abandoned, _ = m.Int64ObservableGauge("audiobook_organizer.ops.abandoned_calls",
		metric.WithUnit("{call}"),
		metric.WithDescription("Uncancellable calls (file stats on a stuck mount, tag parses) abandoned after their time bound whose goroutine has not returned yet, by source. Each holds an OS thread until the mount answers; a value that stays above zero is a dead mount."))
	r.schedLag, _ = m.Float64Histogram("audiobook_organizer.ops.schedule_lag",
		metric.WithUnit("s"),
		metric.WithDescription("Seconds between a scheduled run's due time and the moment the scheduler enqueued it, by def_id."))
	r.schedMiss, _ = m.Int64Counter("audiobook_organizer.ops.schedule_missed",
		metric.WithUnit("{run}"),
		metric.WithDescription("Scheduled fires that came due while the process was down or busy and were folded into one catch-up run, by def_id."))
	r.zombieSrc = map[uint64]func() int64{}
	r.abandonedSrc = map[uint64]abandonedSource{}
	_, _ = m.RegisterCallback(r.observeAbandoned, r.abandoned)
	// One callback for the recorder's lifetime, summing the registered
	// sources: several registries on one provider (tests create many) then
	// add up instead of racing for the single attribute-less point. The
	// error is dropped like the instruments': no callback, no series.
	_, _ = m.RegisterCallback(r.observeZombies, r.zombies)
	return r
}

// observeZombies reports the sum of the registered zombie sources.
func (r *Recorder) observeZombies(_ context.Context, o metric.Observer) error {
	r.zombieMu.Lock()
	srcs := make([]func() int64, 0, len(r.zombieSrc))
	for _, f := range r.zombieSrc {
		srcs = append(srcs, f)
	}
	r.zombieMu.Unlock()
	var n int64
	for _, f := range srcs {
		n += f()
	}
	o.ObserveInt64(r.zombies, n)
	return nil
}

// observeAbandoned reports each registered source's counter, summing sources
// that share a name (several resolvers or tests on one provider).
func (r *Recorder) observeAbandoned(_ context.Context, o metric.Observer) error {
	r.abandonedMu.Lock()
	sums := map[string]int64{}
	for _, src := range r.abandonedSrc {
		sums[src.name] += src.count.Load()
	}
	r.abandonedMu.Unlock()
	for name, n := range sums {
		o.ObserveInt64(r.abandoned, n, metric.WithAttributes(telemetry.Source.String(name)))
	}
	return nil
}

var (
	defaultOnce sync.Once
	defaultRec  *Recorder
)

// Default is the recorder on the global meter provider, created on first use.
func Default() *Recorder {
	defaultOnce.Do(func() { defaultRec = New(otel.GetMeterProvider()) })
	return defaultRec
}

// Started counts a run attempt that began executing.
func (r *Recorder) Started(defID string) {
	r.runs.Add(context.Background(), 1, metric.WithAttributes(
		telemetry.DefID.String(labelFor(defID)), telemetry.Outcome.String(OutcomeStarted)))
}

// Finished records the end of a run attempt this process timed: one
// ops.run.duration observation and then one ops.runs point (in that order; see
// below). An outcome IsRunEnd rejects
// (started, "", anything else) records nothing and Finished reports false.
func (r *Recorder) Finished(defID, outcome string, took time.Duration) bool {
	if !IsRunEnd(outcome) {
		return false
	}
	set := metric.WithAttributes(telemetry.DefID.String(labelFor(defID)), telemetry.Outcome.String(outcome))
	ctx := context.Background()
	// The duration goes in before the run-end count. The two instruments are
	// independent, so no reader sees them atomically, but ops.runs{outcome} is
	// the marker that a run ended: ordering the Record first means any
	// collection that starts after a run-end point is visible also sees that
	// run's duration. Counting first left a window (a preempted goroutine under
	// -race on a loaded runner) where ops.runs{timed_out}=1 read with
	// ops.run.duration{timed_out}=0.
	r.duration.Record(ctx, took.Seconds(), set)
	r.runs.Add(ctx, 1, set)
	return true
}

// Ended records a run end that no attempt in this process timed (the boot
// sweep finishing or dropping a run the previous process left, a run dropped
// before it started): one ops.runs point and no duration, since a made-up
// duration would distort ops.run.duration. Same outcome rule as Finished.
func (r *Recorder) Ended(defID, outcome string) bool {
	if !IsRunEnd(outcome) {
		return false
	}
	r.runs.Add(context.Background(), 1, metric.WithAttributes(
		telemetry.DefID.String(labelFor(defID)), telemetry.Outcome.String(outcome)))
	return true
}

// ScheduleLag records how late a scheduled run was enqueued: the fire time
// minus the due time.
func (r *Recorder) ScheduleLag(defID string, lag time.Duration) {
	r.schedLag.Record(context.Background(), lag.Seconds(), metric.WithAttributes(telemetry.DefID.String(labelFor(defID))))
}

// ScheduleMissed counts n (> 0) due times a schedule skipped before its one
// catch-up run.
func (r *Recorder) ScheduleMissed(defID string, n int64) {
	if n <= 0 {
		return
	}
	r.schedMiss.Add(context.Background(), n, metric.WithAttributes(telemetry.DefID.String(labelFor(defID))))
}

// Items adds n (> 0) items with the given outcome.
func (r *Recorder) Items(defID, outcome string, n int64) {
	if n <= 0 {
		return
	}
	r.items.Add(context.Background(), n, metric.WithAttributes(
		telemetry.DefID.String(labelFor(defID)), telemetry.Outcome.String(outcome)))
}

// Source names for RegisterAbandoned. The set is closed: each is a call site
// that owns a process-wide abandoned counter.
const (
	SourceRepairsResolver     = "repairs_resolver"
	SourceRetiredVersionsStat = "retired_versions_stat"
)

// RegisterAbandoned adds count, the counter a boundedcall.Call feeds with its
// abandoned-goroutine total, to ops.abandoned_calls under the closed source
// name, and returns the function that removes it (idempotent). The series
// exists at 0 from registration. name must come from a small fixed set (a
// call site, never a path or a run id): it becomes a metric attribute.
func (r *Recorder) RegisterAbandoned(name string, count *atomic.Int64) (unregister func()) {
	r.abandonedMu.Lock()
	r.abandonedNext++
	id := r.abandonedNext
	r.abandonedSrc[id] = abandonedSource{name: name, count: count}
	r.abandonedMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			r.abandonedMu.Lock()
			delete(r.abandonedSrc, id)
			r.abandonedMu.Unlock()
		})
	}
}

var activeCallbacks atomic.Int64

// RegisterZombies adds source to the ops.zombies gauge and returns the
// function that removes it (idempotent). The gauge reports the sum of every
// registered source at each collection, so several registries on one meter
// provider never collide, and 0 when none is registered. source is called at
// scrape time, outside the recorder's own lock, so it must be cheap and take
// its own locks. Registering never calls into the SDK, so the caller may hold
// any lock that source does not take.
func (r *Recorder) RegisterZombies(source func() int64) (unregister func()) {
	r.zombieMu.Lock()
	r.zombieNext++
	id := r.zombieNext
	r.zombieSrc[id] = source
	r.zombieMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			r.zombieMu.Lock()
			delete(r.zombieSrc, id)
			r.zombieMu.Unlock()
		})
	}
}

// ActiveInflightCallbacks is the number of in-flight callbacks registered and
// not yet unregistered, process-wide. A leak check for tests.
func ActiveInflightCallbacks() int64 { return activeCallbacks.Load() }

// RegisterInflight registers one callback that reports source() as
// ops.inflight (Runs), ops.progress_age (ProgressAge) and ops.progress_timeout
// (ProgressTimeout) at every collection, and returns the function that
// unregisters it. source returns the authoritative current state per key (it
// is called at scrape time, so it must be cheap and take its own locks). Keys
// that fold to the same label are combined: Runs is summed, ProgressAge and
// ProgressTimeout are each the maximum. (Only unregistered def ids fold, to
// "other"; there the larger budget wins, so the stall alert errs quiet for a
// folded key rather than paging a long-budget def inside its budget.) A key
// seen once and absent later is reported as 0 on every gauge rather than
// vanishing, so a finished run reads 0 instead of leaving the previous value
// in place or dropping the series.
//
// RegisterInflight calls into the SDK (which takes its pipeline lock), and the
// SDK runs the callback under that same lock, so the caller must not hold a
// lock here that source also takes.
func (r *Recorder) RegisterInflight(source func() map[InflightKey]InflightStat) (unregister func()) {
	var mu sync.Mutex
	seen := map[InflightKey]struct{}{}
	reg, err := r.meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		cur := source()
		mu.Lock()
		defer mu.Unlock()
		folded := make(map[InflightKey]InflightStat, len(cur))
		for k, st := range cur {
			fk := InflightKey{DefID: labelFor(k.DefID), Plugin: k.Plugin}
			agg := folded[fk]
			agg.Runs += st.Runs
			agg.ProgressAge = max(agg.ProgressAge, st.ProgressAge)
			agg.ProgressTimeout = max(agg.ProgressTimeout, st.ProgressTimeout)
			folded[fk] = agg
		}
		for k := range seen {
			if _, ok := folded[k]; !ok {
				folded[k] = InflightStat{}
			}
		}
		for k, st := range folded {
			seen[k] = struct{}{}
			set := metric.WithAttributes(telemetry.DefID.String(k.DefID), telemetry.Plugin.String(k.Plugin))
			o.ObserveInt64(r.inflight, st.Runs, set)
			o.ObserveFloat64(r.progAge, st.ProgressAge.Seconds(), set)
			o.ObserveFloat64(r.progTO, st.ProgressTimeout.Seconds(), set)
		}
		return nil
	}, r.inflight, r.progAge, r.progTO)
	if err != nil {
		return func() {}
	}
	activeCallbacks.Add(1)
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = reg.Unregister()
			activeCallbacks.Add(-1)
		})
	}
}
