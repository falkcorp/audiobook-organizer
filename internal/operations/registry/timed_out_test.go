// file: internal/operations/registry/timed_out_test.go
// version: 1.0.0
// guid: 8e03d337-8646-492e-96f0-499394e74e8a
// last-edited: 2026-10-13

package registry_test

// 05-PR2: a run that hits its def's Timeout ends as v3 timed_out (its v2 row
// still reads "canceled" with the timeout: prefix, for the previous binary),
// counts as a failure on the legacy operations_failed_total and as
// ops.runs{outcome="timed_out"} on the OTel meter; interrupted_dropped runs
// count as dropped; interrupted pauses count nothing; and the ops.zombies
// gauge follows the registry's ZombieCount.

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metrics"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
	"github.com/falkcorp/audiobook-organizer/internal/opsmetrics"
	"github.com/falkcorp/audiobook-organizer/internal/telemetry"
)

const opsRuns = "audiobook_organizer.ops.runs"

// newV3OtelRig is a registry on a real Pebble store (so it has v3 run state)
// recording to a private meter provider. The returned rig's store field is
// nil; use the returned *PebbleStore. Shutdown runs at cleanup, before the
// store closes, after the release functions.
func newV3OtelRig(t *testing.T, opts registry.Options, release ...func()) (*otelRig, *database.PebbleStore) {
	t.Helper()
	store := newV3Store(t)
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(append([]sdkmetric.Option{sdkmetric.WithReader(reader)}, telemetry.Views()...)...)
	opts.OpsMetrics = opsmetrics.New(mp)
	if opts.AbandonGrace == 0 {
		opts.AbandonGrace = 50 * time.Millisecond
	}
	if opts.WatchdogInterval == 0 {
		opts.WatchdogInterval = time.Hour
	}
	if opts.SweepInterval == 0 {
		opts.SweepInterval = time.Hour
	}
	if opts.AbandonedCap == 0 {
		opts.AbandonedCap = 100
	}
	reg := registry.NewWithOptions(store, slog.Default(), 2, opts)
	t.Cleanup(func() {
		for _, rel := range release {
			rel()
		}
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = reg.Shutdown(sctx)
	})
	return &otelRig{reader: reader, reg: reg}, store
}

// legacyCounter reads the process-global client_golang counter
// audiobook_organizer_<name>{type=defID}. Tests use a def id of their own, so
// the value is theirs alone.
func legacyCounter(t *testing.T, name, defID string) float64 {
	t.Helper()
	metrics.Register()
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "audiobook_organizer_"+name {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, lp := range m.GetLabel() {
				if lp.GetName() == "type" && lp.GetValue() == defID {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

// runOutcomes is every ops.runs point of defID, keyed by outcome.
func (g *otelRig) runOutcomes(t *testing.T, defID string) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	m, ok := g.collect(t)[opsRuns]
	if !ok {
		return out
	}
	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("ops.runs is %T, want Sum[int64]", m.Data)
	}
	for _, dp := range sum.DataPoints {
		if !attrIs(dp.Attributes, "def_id", defID) {
			continue
		}
		o, _ := dp.Attributes.Value(telemetry.Outcome)
		out[o.AsString()] += dp.Value
	}
	return out
}

func (g *otelRig) zombies(t *testing.T) int64 {
	t.Helper()
	m, ok := g.collect(t)["audiobook_organizer.ops.zombies"]
	if !ok {
		t.Fatal("no ops.zombies series")
	}
	gauge, ok := m.Data.(metricdata.Gauge[int64])
	if !ok || len(gauge.DataPoints) != 1 {
		t.Fatalf("ops.zombies = %+v, want one Gauge[int64] point", m.Data)
	}
	return gauge.DataPoints[0].Value
}

// wantOutcomes fails unless defID's ops.runs points are exactly want.
func wantOutcomes(t *testing.T, g *otelRig, defID string, want map[string]int64) {
	t.Helper()
	got := g.runOutcomes(t, defID)
	if len(got) != len(want) {
		t.Fatalf("ops.runs{def_id=%s} = %v, want %v", defID, got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("ops.runs{def_id=%s} = %v, want %v", defID, got, want)
		}
	}
}

func awaitV3State(t *testing.T, store *database.PebbleStore, id string, want state.V3) *database.RunRecord {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if rec, err := store.GetRunV3(id); err == nil && rec != nil && rec.State == want {
			return rec
		}
		time.Sleep(5 * time.Millisecond)
	}
	rec := runV3(t, store, id)
	t.Fatalf("op %s: v3 state %s, want %s after 5s", id, rec.State, want)
	return nil
}

// TestTimeoutIsTimedOut: a run blocked on ctx.Done() past its Timeout ends
// v3 timed_out, through stopping{timeout} and goroutine_returned (not an
// off-table v2_write), while its v2 row reads canceled with the timeout:
// prefix, and the run's fence is revoked.
func TestTimeoutIsTimedOut(t *testing.T) {
	g, store := newV3OtelRig(t, registry.Options{})
	def := makeValidDef("test.timedout-v3")
	def.Timeout = 10 * time.Millisecond
	var fence atomic.Pointer[registry.Fence]
	def.Run = func(ctx context.Context, _ json.RawMessage, _ registry.Reporter) error {
		f := registry.FenceFromContext(ctx)
		fence.Store(&f)
		<-ctx.Done()
		return ctx.Err()
	}
	if err := g.reg.RegisterOp(def); err != nil {
		t.Fatal(err)
	}
	g.reg.Start(t.Context())
	id, err := g.reg.EnqueueOp(t.Context(), def.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	awaitV2(t, store, id, "canceled")
	rec := awaitV3State(t, store, id, state.V3TimedOut)
	if rec.StopReason != state.StopTimeout {
		t.Errorf("v3 stop reason = %q, want %q", rec.StopReason, state.StopTimeout)
	}
	row, err := store.GetOperationV2(id)
	if err != nil || row == nil || row.ErrorMessage == nil || !strings.HasPrefix(*row.ErrorMessage, state.TimeoutPrefix) {
		t.Fatalf("v2 row = %+v %v, want canceled with a %q message", row, err, state.TimeoutPrefix)
	}
	if ev := eventNames(t, store, id); !strings.HasSuffix(ev, "dispatch stop_requested goroutine_returned") {
		t.Errorf("events = %q, want ... dispatch stop_requested goroutine_returned", ev)
	}
	f := fence.Load()
	if f == nil {
		t.Fatal("Run never captured its fence")
	}
	if !f.Revoked() {
		t.Error("the timed-out run's fence is not revoked")
	}
}

// TestTimeoutCountsAsFailedLegacyAndTimedOutOtel: one timeout is exactly one
// operations_failed_total{type} and one ops.runs{outcome="timed_out"} (plus
// its duration), and not a cancel in either family.
func TestTimeoutCountsAsFailedLegacyAndTimedOutOtel(t *testing.T) {
	g := newOtelRig(t)
	const id = "test.timedout-counts"
	def := makeValidDef(id)
	def.Timeout = 10 * time.Millisecond
	def.Run = func(ctx context.Context, _ json.RawMessage, _ registry.Reporter) error {
		<-ctx.Done()
		return ctx.Err()
	}
	if err := g.reg.RegisterOp(def); err != nil {
		t.Fatal(err)
	}
	failedBefore := legacyCounter(t, "operations_failed_total", id)
	canceledBefore := legacyCounter(t, "operations_canceled_total", id)
	g.reg.Start(t.Context())
	t.Cleanup(func() { _ = g.reg.Shutdown(context.Background()) })
	opID, err := g.reg.EnqueueOp(t.Context(), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	awaitStatus(t, g.store, opID, "canceled", 5*time.Second)
	waitForOtel(t, "runs{timed_out}", func() bool { return g.runOutcomes(t, id)["timed_out"] == 1 })
	wantOutcomes(t, g, id, map[string]int64{"started": 1, "timed_out": 1})
	if got := g.durationCount(t, id, "timed_out"); got != 1 {
		t.Errorf("run.duration{timed_out} observations = %d, want 1", got)
	}
	if d := legacyCounter(t, "operations_failed_total", id) - failedBefore; d != 1 {
		t.Errorf("operations_failed_total{type=%s} moved by %v, want 1", id, d)
	}
	if d := legacyCounter(t, "operations_canceled_total", id) - canceledBefore; d != 0 {
		t.Errorf("operations_canceled_total{type=%s} moved by %v, want 0", id, d)
	}
	t.Logf("ops.runs{def_id=%s} = %v; operations_failed_total delta = 1", id, g.runOutcomes(t, id))
}

// TestTimedOutZombieCountsOnceWhenItReturns: a timed-out run that ignores its
// context is a zombie; it is counted, as timed_out and a legacy failure, once,
// when its goroutine returns (the zombie monitor counted nothing before
// 05-PR2), and its v3 record ends timed_out.
func TestTimedOutZombieCountsOnceWhenItReturns(t *testing.T) {
	s := newStubbornRun()
	g, store := newV3OtelRig(t, registry.Options{}, s.releaseRun)
	const id = "test.timedout-zombie-counts"
	def := makeValidDef(id)
	def.Timeout = 50 * time.Millisecond
	def.Run = s.run
	if err := g.reg.RegisterOp(def); err != nil {
		t.Fatal(err)
	}
	failedBefore := legacyCounter(t, "operations_failed_total", id)
	g.reg.Start(t.Context())
	opID, err := g.reg.EnqueueOp(t.Context(), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	recv(t, s.started, "run to start")
	awaitZombieCount(t, g.reg, 1)
	if got := g.runOutcomes(t, id)["timed_out"]; got != 0 {
		t.Fatalf("runs{timed_out} = %d while the goroutine still runs, want 0", got)
	}
	s.releaseRun()
	awaitV2(t, store, opID, "canceled")
	awaitV3State(t, store, opID, state.V3TimedOut)
	waitForOtel(t, "runs{timed_out}", func() bool { return g.runOutcomes(t, id)["timed_out"] == 1 })
	wantOutcomes(t, g, id, map[string]int64{"started": 1, "timed_out": 1})
	if d := legacyCounter(t, "operations_failed_total", id) - failedBefore; d != 1 {
		t.Errorf("operations_failed_total{type=%s} moved by %v, want 1", id, d)
	}
}

// TestUserCancelStillCanceled: a user cancel is outcome canceled, v3
// canceled, the legacy canceled counter, and not a failure.
func TestUserCancelStillCanceled(t *testing.T) {
	g, store := newV3OtelRig(t, registry.Options{})
	const id = "test.timedout-user-cancel"
	started := make(chan struct{})
	def := makeValidDef(id)
	def.Run = func(ctx context.Context, _ json.RawMessage, _ registry.Reporter) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	if err := g.reg.RegisterOp(def); err != nil {
		t.Fatal(err)
	}
	failedBefore := legacyCounter(t, "operations_failed_total", id)
	canceledBefore := legacyCounter(t, "operations_canceled_total", id)
	g.reg.Start(t.Context())
	opID, err := g.reg.EnqueueOp(t.Context(), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	recv(t, started, "run to start")
	if err := g.reg.Cancel(opID); err != nil {
		t.Fatal(err)
	}
	awaitV2(t, store, opID, "canceled")
	awaitV3State(t, store, opID, state.V3Canceled)
	waitForOtel(t, "runs{canceled}", func() bool { return g.runOutcomes(t, id)["canceled"] == 1 })
	wantOutcomes(t, g, id, map[string]int64{"started": 1, "canceled": 1})
	if d := legacyCounter(t, "operations_canceled_total", id) - canceledBefore; d != 1 {
		t.Errorf("operations_canceled_total{type=%s} moved by %v, want 1", id, d)
	}
	if d := legacyCounter(t, "operations_failed_total", id) - failedBefore; d != 0 {
		t.Errorf("operations_failed_total{type=%s} moved by %v, want 0", id, d)
	}
}

// shutdownDuringRun starts def, waits for it to run, and shuts the registry
// down; Run returns on ctx.Done().
func shutdownDuringRun(t *testing.T, g *otelRig, def registry.OperationDef) string {
	t.Helper()
	started := make(chan struct{})
	def.Run = func(ctx context.Context, _ json.RawMessage, _ registry.Reporter) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	if err := g.reg.RegisterOp(def); err != nil {
		t.Fatal(err)
	}
	g.reg.Start(t.Context())
	opID, err := g.reg.EnqueueOp(t.Context(), def.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	recv(t, started, "run to start")
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := g.reg.Shutdown(sctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	return opID
}

// TestInterruptedPauseRecordsNoOutcome: a shutdown during a ResumeRestart run
// writes interrupted_quiesced, a pause, and records no ops.runs outcome (only
// started) and no legacy outcome counter.
func TestInterruptedPauseRecordsNoOutcome(t *testing.T) {
	g, store := newV3OtelRig(t, registry.Options{})
	const id = "test.timedout-pause"
	def := makeValidDef(id)
	def.ResumePolicy = registry.ResumeRestart
	before := map[string]float64{}
	for _, n := range []string{"operations_completed_total", "operations_failed_total", "operations_canceled_total"} {
		before[n] = legacyCounter(t, n, id)
	}
	opID := shutdownDuringRun(t, g, def)
	if got := v2Status(t, store, opID); got != "interrupted_quiesced" {
		t.Fatalf("status after shutdown = %q, want interrupted_quiesced", got)
	}
	wantOutcomes(t, g, id, map[string]int64{"started": 1})
	for n, b := range before {
		if d := legacyCounter(t, n, id) - b; d != 0 {
			t.Errorf("%s{type=%s} moved by %v on a pause, want 0", n, id, d)
		}
	}
}

// TestDroppedRecordsOutcome: a ResumeDrop run ends dropped, and is counted
// once as ops.runs{outcome="dropped"}, both when the boot sweep drops the run
// a crashed process left running (no attempt, so no duration) and when a
// shutdown stops it mid-run (with the attempt's duration).
func TestDroppedRecordsOutcome(t *testing.T) {
	t.Run("at boot", func(t *testing.T) {
		g, store := newV3OtelRig(t, registry.Options{})
		const id = "test.timedout-drop-boot"
		def := makeValidDef(id) // ResumeDrop
		opID := seedStoppingRun(t, store, id, "")
		var runs atomic.Int32
		def.Run = func(context.Context, json.RawMessage, registry.Reporter) error { runs.Add(1); return nil }
		if err := g.reg.RegisterOp(def); err != nil {
			t.Fatal(err)
		}
		g.reg.Start(t.Context())
		awaitV2(t, store, opID, "interrupted_dropped")
		awaitV3State(t, store, opID, state.V3Dropped)
		waitForOtel(t, "runs{dropped}", func() bool { return g.runOutcomes(t, id)["dropped"] == 1 })
		wantOutcomes(t, g, id, map[string]int64{"dropped": 1})
		if got := g.durationCount(t, id, "dropped"); got != 0 {
			t.Errorf("run.duration{dropped} observations = %d for a boot drop, want 0", got)
		}
		if n := runs.Load(); n != 0 {
			t.Errorf("Run called %d times, want 0", n)
		}
	})
	t.Run("at shutdown", func(t *testing.T) {
		g, store := newV3OtelRig(t, registry.Options{})
		const id = "test.timedout-drop-shutdown"
		opID := shutdownDuringRun(t, g, makeValidDef(id)) // ResumeDrop
		if got := v2Status(t, store, opID); got != "interrupted_dropped" {
			t.Fatalf("status after shutdown = %q, want interrupted_dropped", got)
		}
		wantOutcomes(t, g, id, map[string]int64{"started": 1, "dropped": 1})
		if got := g.durationCount(t, id, "dropped"); got != 1 {
			t.Errorf("run.duration{dropped} observations = %d, want 1", got)
		}
	})
}

// TestZombieGaugeTracksCount: a run that ignores its context and outlives the
// abandon grace makes ops.zombies 1; when its goroutine returns the gauge
// reads 0 again. Run with -race: the gauge callback reads r.running from the
// collecting goroutine while the workers change it.
func TestZombieGaugeTracksCount(t *testing.T) {
	s := newStubbornRun()
	g, _ := newV3OtelRig(t, registry.Options{}, s.releaseRun)
	def := makeValidDef("test.timedout-zombie-gauge")
	def.Run = s.run
	if err := g.reg.RegisterOp(def); err != nil {
		t.Fatal(err)
	}
	g.reg.Start(t.Context())
	if got := g.zombies(t); got != 0 {
		t.Fatalf("ops.zombies before any run = %d, want 0", got)
	}
	id, err := g.reg.EnqueueOp(t.Context(), def.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	recv(t, s.started, "run to start")
	if err := g.reg.Cancel(id); err != nil {
		t.Fatal(err)
	}
	awaitZombieCount(t, g.reg, 1)
	if got := g.zombies(t); got != 1 {
		t.Fatalf("ops.zombies with one zombie = %d, want 1", got)
	}
	s.releaseRun()
	awaitZombieCount(t, g.reg, 0)
	if got := g.zombies(t); got != 0 {
		t.Fatalf("ops.zombies after the goroutine returned = %d, want 0", got)
	}
}

// TestShutdownUnregistersZombieSource: a registry started and shut down
// leaves no ops.zombies source behind.
func TestShutdownUnregistersZombieSource(t *testing.T) {
	before := opsmetrics.ActiveZombieSources()
	for i := 0; i < 5; i++ {
		g := newOtelRig(t)
		g.reg.Start(context.Background())
		if got := opsmetrics.ActiveZombieSources(); got != before+1 {
			t.Fatalf("round %d: %d zombie sources after Start, want %d", i, got, before+1)
		}
		if err := g.reg.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := opsmetrics.ActiveZombieSources(); got != before {
			t.Fatalf("round %d: %d zombie sources after Shutdown, want %d", i, got, before)
		}
	}
}
