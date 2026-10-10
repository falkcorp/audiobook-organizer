// file: internal/opsmetrics/opsmetrics.go
// version: 1.0.0
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

// Recorder holds the instruments of one meter provider.
type Recorder struct {
	meter    metric.Meter
	runs     metric.Int64Counter
	duration metric.Float64Histogram
	items    metric.Int64Counter
	inflight metric.Int64ObservableGauge
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
		metric.WithDescription("Operation runs currently holding a worker slot, by def_id and plugin."))
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

// RegisterInflight registers a callback that reports source() as ops.inflight
// at every collection and returns the function that unregisters it. source
// returns the authoritative current count per key (it is called at scrape time,
// so it must be cheap and take its own locks). A key seen once and absent later
// is reported as 0 rather than vanishing, so a finished run reads 0 instead of
// leaving the previous value in place or dropping the series.
func (r *Recorder) RegisterInflight(source func() map[InflightKey]int64) (unregister func()) {
	var mu sync.Mutex
	seen := map[InflightKey]struct{}{}
	reg, err := r.meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		cur := source()
		mu.Lock()
		defer mu.Unlock()
		folded := make(map[InflightKey]int64, len(cur))
		for k, n := range cur {
			folded[InflightKey{DefID: labelFor(k.DefID), Plugin: k.Plugin}] += n
		}
		for k := range seen {
			if _, ok := folded[k]; !ok {
				folded[k] = 0
			}
		}
		for k, n := range folded {
			seen[k] = struct{}{}
			o.ObserveInt64(r.inflight, n, metric.WithAttributes(
				telemetry.DefID.String(k.DefID), telemetry.Plugin.String(k.Plugin)))
		}
		return nil
	}, r.inflight)
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
