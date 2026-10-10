// file: internal/opsmetrics/opsmetrics.go
// version: 1.2.0
// guid: 0b6e9c35-18d4-4f7a-b2c1-6a93e5d07f48
// last-edited: 2026-10-10

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
// runs outcomes: started, completed, failed, canceled, timed_out, dropped. The
// registry records started, completed, failed and canceled today. timed_out
// and dropped are declared values nothing can reach yet: a deadline hit is
// finalized as canceled and interrupted_dropped never reaches the metrics hook
// (05-PR2 changes both).
//
// items outcomes: processed, ok, failed, skipped. The v2 registry can only
// count progress, so it records processed; the v3 runner (05-PR5) records ok,
// failed and skipped. NOT the legacy gauge audiobook_organizer_op_items_total
// (no "s" in "ops"; per-run op_id label, emitted by internal/metrics).
//
// Declared but not wired here: see Unwired. They have no recorder yet because
// an OTel instrument created without a golden row and a seed fails the series
// contract test (internal/telemetry/contract); 05-PR5 adds each with its row.
package opsmetrics

import (
	"context"
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
	{"audiobook_organizer.ops.zombies", "gauge", "{run}", "05-PR5"},
	{"audiobook_organizer.ops.checkpoint_age", "gauge", "s", "05-PR5"},
	{"audiobook_organizer.ops.schedule_lag", "histogram", "s", "05-PR5"},
	{"audiobook_organizer.ops.schedule_missed", "counter", "{run}", "05-PR5"},
}

// OutcomeFor maps a run status to the outcome recorded for it. ok is false for
// every status that records nothing: the interrupted_* family is a pause, not
// an end (the resumed attempt reports its own outcome), non-terminal statuses
// are not outcomes, and "timeout" is not produced at HEAD (a deadline hit is
// finalized as canceled).
func OutcomeFor(status string) (outcome string, ok bool) {
	switch status {
	case string(state.Completed):
		return OutcomeCompleted, true
	case string(state.Failed):
		return OutcomeFailed, true
	case string(state.Canceled):
		return OutcomeCanceled, true
	}
	return "", false
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
	meter    metric.Meter
	runs     metric.Int64Counter
	duration metric.Float64Histogram
	items    metric.Int64Counter
	inflight metric.Int64ObservableGauge
	progAge  metric.Float64ObservableGauge
	progTO   metric.Float64ObservableGauge
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
	return r
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

// Finished records the end of a run attempt: one ops.runs point and one
// ops.run.duration observation. A status OutcomeFor rejects records nothing and
// Finished reports false.
func (r *Recorder) Finished(defID, status string, took time.Duration) bool {
	outcome, ok := OutcomeFor(status)
	if !ok {
		return false
	}
	set := metric.WithAttributes(telemetry.DefID.String(labelFor(defID)), telemetry.Outcome.String(outcome))
	ctx := context.Background()
	r.runs.Add(ctx, 1, set)
	r.duration.Record(ctx, took.Seconds(), set)
	return true
}

// Items adds n (> 0) items with the given outcome.
func (r *Recorder) Items(defID, outcome string, n int64) {
	if n <= 0 {
		return
	}
	r.items.Add(context.Background(), n, metric.WithAttributes(
		telemetry.DefID.String(labelFor(defID)), telemetry.Outcome.String(outcome)))
}

var activeCallbacks atomic.Int64

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
