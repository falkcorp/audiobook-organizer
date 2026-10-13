// file: internal/operations/registry/timed_out_test.go
// version: 1.2.0
// guid: 8e03d337-8646-492e-96f0-499394e74e8a
// last-edited: 2026-10-13

package registry_test

// 05-PR2: a run that hits its def's Timeout ends as v3 timed_out (its v2 row
// still reads "canceled" with the timeout: prefix, for the previous binary),
// counts as a failure on the legacy operations_failed_total and as
// ops.runs{outcome="timed_out"} on the OTel meter; interrupted_dropped runs
// count as dropped; interrupted pauses count nothing; every run end is counted
// exactly once, by whichever of the worker, the zombie monitor, Shutdown's
// stamp and the next boot's sweep finishes its row; and the ops.zombies gauge
// follows the registry's ZombieCount.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/falkcorp/audiobook-organizer/internal/database"
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

// privateMeter is a recorder on its own meter provider, and a rig that reads
// it, for tests that build their registries by hand (several on one store,
// or on a custom store).
func privateMeter() (*opsmetrics.Recorder, *otelRig) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(append([]sdkmetric.Option{sdkmetric.WithReader(reader)}, telemetry.Views()...)...)
	return opsmetrics.New(mp), &otelRig{reader: reader}
}

// awaitNotRunning waits until opID has no handle in r.
func awaitNotRunning(t *testing.T, r *registry.Registry, opID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for r.IsRunning(opID) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if r.IsRunning(opID) {
		t.Fatalf("op %s still has a handle after 5s", opID)
	}
}

// TestShutdownStampedTimedOutZombieCountsTimedOutOnce: a timed-out zombie is
// stamped by Shutdown with its decided status (canceled + timeout:); its
// goroutine then returns inside Shutdown's wait, and the zombie monitor finds
// the stamp, keeps it and counts it. The kept row's outcome is read off the
// row, so the run is timed_out once, never canceled. Mutation check: make the
// kept-cancel branch of writeRunTerminalStatus report OutcomeCanceled.
func TestShutdownStampedTimedOutZombieCountsTimedOutOnce(t *testing.T) {
	s := newStubbornRun()
	g, store := newV3OtelRig(t, registry.Options{}, s.releaseRun)
	const id = "test.timedout-stamped-zombie"
	def := makeValidDef(id)
	def.Timeout = 30 * time.Millisecond
	def.Run = s.run
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
	recv(t, s.started, "run to start")
	awaitZombieCount(t, g.reg, 1)
	// Release inside Shutdown's goroutine wait: once its stamp has landed
	// (the row reads canceled), well before the 2s escape, so the monitor's
	// write always finds the stamp and counts first. A fixed delay let a slow
	// runner release before the stamp, and the kept-cancel branch went
	// unexercised.
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if row, err := store.GetOperationV2(opID); err == nil && row != nil && row.Status == "canceled" {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		s.releaseRun()
	}()
	sctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = g.reg.Shutdown(sctx)
	awaitV2(t, store, opID, "canceled")
	if rec := runV3(t, store, opID); rec.State != state.V3TimedOut {
		t.Fatalf("v3 state = %s, want timed_out", rec.State)
	}
	wantOutcomes(t, g, id, map[string]int64{"started": 1, "timed_out": 1})
	if d := legacyCounter(t, "operations_failed_total", id) - failedBefore; d != 1 {
		t.Errorf("operations_failed_total{type=%s} moved by %v, want 1", id, d)
	}
	if d := legacyCounter(t, "operations_canceled_total", id) - canceledBefore; d != 0 {
		t.Errorf("operations_canceled_total{type=%s} moved by %v, want 0", id, d)
	}
}

// TestShutdownCountsAStampedRunThatNeverReturns: a ResumeDrop run that
// ignores its context does not drain, so Shutdown stamps it
// interrupted_dropped. Its goroutine is still running when Shutdown returns,
// so the run's own exit will never count it, and the next boot will not
// either (the row is no longer running): Shutdown counts it, once, and the
// late return adds nothing. Mutation check: drop countShutdownStamps.
func TestShutdownCountsAStampedRunThatNeverReturns(t *testing.T) {
	s := newStubbornRun()
	g, store := newV3OtelRig(t, registry.Options{}, s.releaseRun)
	const id = "test.timedout-stamped-drop"
	def := makeValidDef(id) // ResumeDrop
	def.Run = s.run
	if err := g.reg.RegisterOp(def); err != nil {
		t.Fatal(err)
	}
	g.reg.Start(t.Context())
	opID, err := g.reg.EnqueueOp(t.Context(), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	recv(t, s.started, "run to start")
	sctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = g.reg.Shutdown(sctx)
	if got := v2Status(t, store, opID); got != "interrupted_dropped" {
		t.Fatalf("status after Shutdown = %q, want interrupted_dropped", got)
	}
	wantOutcomes(t, g, id, map[string]int64{"started": 1, "dropped": 1})
	s.releaseRun()
	recv(t, s.finished, "the stubborn run to return")
	awaitNotRunning(t, g.reg, opID)
	wantOutcomes(t, g, id, map[string]int64{"started": 1, "dropped": 1})
}

// TestShutdownStampsATimedOutRunAsATimeout: a run that hit its deadline and
// is still inside its abandon grace when Shutdown's context expires is stamped
// as a timeout (canceled + timeout:, v3 timed_out) and counted timed_out, not
// stamped interrupted_quiesced, which lost the deadline and made the run
// resumable on the next boot.
func TestShutdownStampsATimedOutRunAsATimeout(t *testing.T) {
	s := newStubbornRun()
	g, store := newV3OtelRig(t, registry.Options{AbandonGrace: time.Second}, s.releaseRun)
	const id = "test.timedout-shutdown-in-grace"
	def := makeValidDef(id)
	def.ResumePolicy = registry.ResumeRestart
	def.Timeout = 20 * time.Millisecond
	def.Run = s.run
	if err := g.reg.RegisterOp(def); err != nil {
		t.Fatal(err)
	}
	g.reg.Start(t.Context())
	opID, err := g.reg.EnqueueOp(t.Context(), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	recv(t, s.started, "run to start")
	time.Sleep(150 * time.Millisecond) // past the deadline, inside the 1s grace
	sctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = g.reg.Shutdown(sctx)
	row, err := store.GetOperationV2(opID)
	if err != nil || row == nil {
		t.Fatalf("GetOperationV2: %v %v", row, err)
	}
	if row.Status != "canceled" || row.ErrorMessage == nil || !strings.HasPrefix(*row.ErrorMessage, state.TimeoutPrefix) {
		t.Fatalf("row after Shutdown = %q %v, want canceled with a %q message", row.Status, row.ErrorMessage, state.TimeoutPrefix)
	}
	if rec := runV3(t, store, opID); rec.State != state.V3TimedOut {
		t.Errorf("v3 state = %s, want timed_out", rec.State)
	}
	wantOutcomes(t, g, id, map[string]int64{"started": 1, "timed_out": 1})
	s.releaseRun()
	recv(t, s.finished, "the stubborn run to return")
	awaitNotRunning(t, g.reg, opID)
}

// TestBootSweepCountsStoppedRunsItFinishes: a run the previous process
// stopped for a timeout, or a user cancel, and left stopping is finished by
// the boot sweep, and counted there: timed_out (a legacy failure) and
// canceled, with no duration. A row whose def is not registered is counted
// in ops.runs only, under def_id="other", and mints no legacy series.
// Mutation check: make endStoppedRunAtBoot count every final as canceled.
func TestBootSweepCountsStoppedRunsItFinishes(t *testing.T) {
	g, store := newV3OtelRig(t, registry.Options{})
	const timedOutDef, canceledDef, unknownDef = "test.timedout-boot-timeout", "test.timedout-boot-user", "test.timedout-boot-unknown"
	timedOutID := seedStoppingRun(t, store, timedOutDef, state.StopTimeout)
	canceledID := seedStoppingRun(t, store, canceledDef, state.StopUser)
	unknownID := seedStoppingRun(t, store, unknownDef, state.StopTimeout)
	for _, d := range []string{timedOutDef, canceledDef} {
		def := makeValidDef(d)
		def.ResumePolicy = registry.ResumeRestart
		if err := g.reg.RegisterOp(def); err != nil {
			t.Fatal(err)
		}
	}
	failedBefore := legacyCounter(t, "operations_failed_total", timedOutDef)
	canceledBefore := legacyCounter(t, "operations_canceled_total", canceledDef)
	g.reg.Start(t.Context())
	for _, id := range []string{timedOutID, canceledID, unknownID} {
		awaitV2(t, store, id, "canceled")
	}
	waitForOtel(t, "boot-sweep run ends", func() bool {
		return g.runOutcomes(t, timedOutDef)["timed_out"] == 1 && g.runOutcomes(t, canceledDef)["canceled"] == 1
	})
	wantOutcomes(t, g, timedOutDef, map[string]int64{"timed_out": 1})
	wantOutcomes(t, g, canceledDef, map[string]int64{"canceled": 1})
	wantOutcomes(t, g, opsmetrics.OtherDefID, map[string]int64{"timed_out": 1})
	if n := g.durationCount(t, timedOutDef, "timed_out") + g.durationCount(t, canceledDef, "canceled"); n != 0 {
		t.Errorf("run.duration has %d observations for boot-sweep ends, want 0", n)
	}
	if d := legacyCounter(t, "operations_failed_total", timedOutDef) - failedBefore; d != 1 {
		t.Errorf("operations_failed_total{type=%s} moved by %v, want 1", timedOutDef, d)
	}
	if d := legacyCounter(t, "operations_canceled_total", canceledDef) - canceledBefore; d != 1 {
		t.Errorf("operations_canceled_total{type=%s} moved by %v, want 1", canceledDef, d)
	}
	if v := legacyCounter(t, "operations_failed_total", unknownDef); v != 0 {
		t.Errorf("operations_failed_total{type=%s} = %v for an unregistered def, want no series", unknownDef, v)
	}
}

// TestForceDropCountsDropped: a row restarted three times without progress is
// force-dropped at pickup, before any attempt runs: one dropped, no started,
// no duration. Mutation check: drop the force-drop's recordRunEnd.
func TestForceDropCountsDropped(t *testing.T) {
	g := newOtelRig(t)
	const id = "test.timedout-force-drop"
	var runs atomic.Int32
	def := makeValidDef(id)
	def.ResumePolicy = registry.ResumeRestart
	def.Run = func(context.Context, json.RawMessage, registry.Reporter) error { runs.Add(1); return nil }
	if err := g.reg.RegisterOp(def); err != nil {
		t.Fatal(err)
	}
	// Enqueued and marked before Start, as TestWatchdog_InfiniteRestartForceDrop
	// does, so the first dispatch already sees resume_count=3.
	opID, err := g.reg.EnqueueOp(t.Context(), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	g.store.setResumeCount(opID, 3)
	g.reg.Start(t.Context())
	t.Cleanup(func() { _ = g.reg.Shutdown(context.Background()) })
	awaitStatus(t, g.store, opID, "interrupted_dropped", 5*time.Second)
	waitForOtel(t, "runs{dropped}", func() bool { return g.runOutcomes(t, id)["dropped"] == 1 })
	wantOutcomes(t, g, id, map[string]int64{"dropped": 1})
	if got := g.durationCount(t, id, "dropped"); got != 0 {
		t.Errorf("run.duration{dropped} observations = %d, want 0", got)
	}
	if n := runs.Load(); n != 0 {
		t.Errorf("Run called %d times, want 0", n)
	}
}

// TestScanQuiescedBeforeStartWithResumeDropCountsDropped: the scan
// stand-down pickup gate writes interruptedStatus(ResumePolicy), which is
// interrupted_dropped for a ResumeDrop def: the run ends before it starts and
// counts one dropped. (Production library.scan is ResumeRestart, so its
// pickup-gate stop is interrupted_quiesced, a pause, and counts nothing; the
// gate itself is policy-generic.) Mutation check: drop the dropped count in
// the pickup gate.
func TestScanQuiescedBeforeStartWithResumeDropCountsDropped(t *testing.T) {
	rec, g := privateMeter()
	store := newInterleaveStore("op-target")
	reg := registry.NewWithOptions(store, slog.Default(), 2, registry.Options{OpsMetrics: rec})
	g.reg = reg
	store.r = reg
	var runs atomic.Int32
	def := makeValidDef(registry.ScanStandDownDefIDForTest) // ResumeDrop
	def.Run = func(context.Context, json.RawMessage, registry.Reporter) error { runs.Add(1); return nil }
	if err := reg.RegisterOp(def); err != nil {
		t.Fatal(err)
	}
	reg.Start(t.Context())
	t.Cleanup(func() {
		if store.releaseGate != nil {
			store.releaseGate()
		}
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = reg.Shutdown(sctx)
	})
	store.raiseGate.Store(true)
	store.insertQueuedAtomic(database.OperationV2Row{
		ID: store.target, DefID: def.ID, Plugin: def.Plugin, Status: "queued", QueuedAt: time.Now().UTC(),
	})
	reg.DispatchCycleForTest(t.Context())
	recv(t, store.gateRaised, "the scan gate to go up between claim and pickup")
	awaitStatus(t, store.fakeStore, store.target, "interrupted_dropped", 5*time.Second)
	waitForOtel(t, "runs{dropped}", func() bool { return g.runOutcomes(t, def.ID)["dropped"] == 1 })
	wantOutcomes(t, g, def.ID, map[string]int64{"dropped": 1})
	if n := runs.Load(); n != 0 {
		t.Errorf("Run called %d times, want 0", n)
	}
}

// finalWriteFailingStore fails the worker's terminal write running ->
// failStatus while armed.
type finalWriteFailingStore struct {
	*database.PebbleStore
	failStatus string
	armed      atomic.Bool
}

func (s *finalWriteFailingStore) UpdateOperationV2StatusIf(id, from, status string, startedAt, completedAt *time.Time, errMsg *string) (bool, error) {
	if s.armed.Load() && from == "running" && status == s.failStatus {
		return false, errors.New("injected terminal write failure")
	}
	return s.PebbleStore.UpdateOperationV2StatusIf(id, from, status, startedAt, completedAt, errMsg)
}

// TestUnwrittenTimeoutIsCountedOnceByTheBootSweep: a timed-out run whose
// terminal write fails is left v3 stopping{timeout} with its row "running".
// The worker does not count it; the next start's boot sweep finishes it
// timed_out and counts it there, so the run is timed_out once, not twice.
// Mutation check: drop the !out.written gate in recordRunMetrics.
func TestUnwrittenTimeoutIsCountedOnceByTheBootSweep(t *testing.T) {
	store := newV3Store(t)
	rec, g := privateMeter()
	const id = "test.timedout-unwritten"
	failing := &finalWriteFailingStore{PebbleStore: store, failStatus: "canceled"}
	failing.armed.Store(true)
	newReg := func(st database.OpsV2Store) *registry.Registry {
		reg := registry.NewWithOptions(st, slog.Default(), 2, registry.Options{
			OpsMetrics: rec, AbandonGrace: 50 * time.Millisecond, WatchdogInterval: time.Hour, SweepInterval: time.Hour,
		})
		def := makeValidDef(id)
		def.Timeout = 10 * time.Millisecond
		def.Run = func(ctx context.Context, _ json.RawMessage, _ registry.Reporter) error {
			<-ctx.Done()
			return ctx.Err()
		}
		if err := reg.RegisterOp(def); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = reg.Shutdown(sctx)
		})
		return reg
	}
	failedBefore := legacyCounter(t, "operations_failed_total", id)

	first := newReg(failing)
	g.reg = first
	first.Start(t.Context())
	opID, err := first.EnqueueOp(t.Context(), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	awaitV3State(t, store, opID, state.V3Stopping)
	awaitNotRunning(t, first, opID)
	if got := v2Status(t, store, opID); got != "running" {
		t.Fatalf("row after the failed terminal write = %q, want running", got)
	}
	if got := g.runOutcomes(t, id)["timed_out"]; got != 0 {
		t.Fatalf("runs{timed_out} = %d after a terminal write that failed, want 0", got)
	}
	if err := first.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	second := newReg(store)
	second.Start(t.Context())
	awaitV2(t, store, opID, "canceled")
	awaitV3State(t, store, opID, state.V3TimedOut)
	waitForOtel(t, "runs{timed_out}", func() bool { return g.runOutcomes(t, id)["timed_out"] == 1 })
	wantOutcomes(t, g, id, map[string]int64{"started": 1, "timed_out": 1})
	if d := legacyCounter(t, "operations_failed_total", id) - failedBefore; d != 1 {
		t.Errorf("operations_failed_total{type=%s} moved by %v, want 1", id, d)
	}
}

// TestUnwrittenCompletionIsCountedOnceByTheBootSweep: the !written rule is
// not limited to runs recorded v3 stopping. A ResumeDrop run that completes
// but whose terminal write fails leaves its row "running"; the next start's
// boot sweep drops it and counts it dropped. Counting it completed here too
// counted one run twice, with two different outcomes. Mutation check: gate
// the !out.written skip on h.stoppingRecorded as well.
func TestUnwrittenCompletionIsCountedOnceByTheBootSweep(t *testing.T) {
	store := newV3Store(t)
	rec, g := privateMeter()
	const id = "test.timedout-unwritten-completion"
	failing := &finalWriteFailingStore{PebbleStore: store, failStatus: "completed"}
	failing.armed.Store(true)
	newReg := func(st database.OpsV2Store) *registry.Registry {
		reg := registry.NewWithOptions(st, slog.Default(), 2, registry.Options{
			OpsMetrics: rec, AbandonGrace: 50 * time.Millisecond, WatchdogInterval: time.Hour, SweepInterval: time.Hour,
		})
		def := makeValidDef(id) // ResumeDrop
		def.Run = func(context.Context, json.RawMessage, registry.Reporter) error { return nil }
		if err := reg.RegisterOp(def); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = reg.Shutdown(sctx)
		})
		return reg
	}
	completedBefore := legacyCounter(t, "operations_completed_total", id)

	first := newReg(failing)
	g.reg = first
	first.Start(t.Context())
	opID, err := first.EnqueueOp(t.Context(), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitForOtel(t, "runs{started}", func() bool { return g.runOutcomes(t, id)["started"] == 1 })
	awaitNotRunning(t, first, opID)
	if got := v2Status(t, store, opID); got != "running" {
		t.Fatalf("row after the failed terminal write = %q, want running", got)
	}
	wantOutcomes(t, g, id, map[string]int64{"started": 1})
	if err := first.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	second := newReg(store)
	second.Start(t.Context())
	awaitV2(t, store, opID, "interrupted_dropped")
	waitForOtel(t, "runs{dropped}", func() bool { return g.runOutcomes(t, id)["dropped"] == 1 })
	wantOutcomes(t, g, id, map[string]int64{"started": 1, "dropped": 1})
	if d := legacyCounter(t, "operations_completed_total", id) - completedBefore; d != 0 {
		t.Errorf("operations_completed_total{type=%s} moved by %v, want 0", id, d)
	}
}

// flakyFakeStore is a fakeStore that can, for one op id, move the row to
// "canceled" (as Cancel's cancelByRow would) in place of the worker's
// running -> completed write and then fail its retry from canceled, or fail
// every read of the row.
type flakyFakeStore struct {
	*fakeStore
	cancelThenFail atomic.Bool
	failRead       atomic.Bool
}

func (s *flakyFakeStore) UpdateOperationV2StatusIf(id, from, status string, startedAt, completedAt *time.Time, errMsg *string) (bool, error) {
	if s.cancelThenFail.Load() && status == "completed" {
		switch from {
		case "running":
			now := time.Now().UTC()
			msg := "canceled by user"
			if err := s.fakeStore.UpdateOperationV2Status(id, "canceled", nil, &now, &msg); err != nil {
				return false, err
			}
			return false, nil
		case "canceled":
			return false, errors.New("injected write failure")
		}
	}
	return s.fakeStore.UpdateOperationV2StatusIf(id, from, status, startedAt, completedAt, errMsg)
}

func (s *flakyFakeStore) GetOperationV2(id string) (*database.OperationV2Row, error) {
	if s.failRead.Load() {
		return nil, errors.New("injected read failure")
	}
	return s.fakeStore.GetOperationV2(id)
}

// TestCanceledRowThenFailedWriteCountsTheCancel: the run completed, but
// Cancel moved its row to "canceled" between the handle release and the
// terminal write, and the worker's retry from canceled then failed. The row
// is final (canceled, never resumable) and Cancel counts nothing, so the
// worker counts the end the row reads: canceled, once. Before the fix the
// failed retry was counted nowhere.
func TestCanceledRowThenFailedWriteCountsTheCancel(t *testing.T) {
	rec, g := privateMeter()
	store := &flakyFakeStore{fakeStore: newFakeStore()}
	reg := registry.NewWithOptions(store, slog.Default(), 1, registry.Options{OpsMetrics: rec})
	g.reg = reg
	const id = "test.timedout-cancel-then-fail"
	def := makeValidDef(id)
	if err := reg.RegisterOp(def); err != nil {
		t.Fatal(err)
	}
	canceledBefore := legacyCounter(t, "operations_canceled_total", id)
	store.cancelThenFail.Store(true)
	reg.Start(t.Context())
	t.Cleanup(func() { _ = reg.Shutdown(context.Background()) })
	opID, err := reg.EnqueueOp(t.Context(), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	awaitStatus(t, store.fakeStore, opID, "canceled", 5*time.Second)
	waitForOtel(t, "runs{canceled}", func() bool { return g.runOutcomes(t, id)["canceled"] == 1 })
	awaitNotRunning(t, reg, opID)
	wantOutcomes(t, g, id, map[string]int64{"started": 1, "canceled": 1})
	if d := legacyCounter(t, "operations_canceled_total", id) - canceledBefore; d != 1 {
		t.Errorf("operations_canceled_total{type=%s} moved by %v, want 1", id, d)
	}
}

// TestCanceledZombieUnreadableRowCountsTheCancel: a zombie whose stop was
// not recorded in v3 (no v3 state in this store) is canceled again; Cancel
// then writes the row "canceled" itself and counts nothing. When the
// goroutine returns, the monitor's write from running is refused and its
// re-read fails. Not shutting down, the refusal can only be that cancel, so
// the monitor counts the run's decided outcome, canceled, once.
func TestCanceledZombieUnreadableRowCountsTheCancel(t *testing.T) {
	rec, g := privateMeter()
	store := &flakyFakeStore{fakeStore: newFakeStore()}
	s := newStubbornRun()
	reg := registry.NewWithOptions(store, slog.Default(), 2, registry.Options{
		OpsMetrics: rec, AbandonGrace: 50 * time.Millisecond, WatchdogInterval: time.Hour, SweepInterval: time.Hour, AbandonedCap: 100,
	})
	g.reg = reg
	t.Cleanup(func() {
		store.failRead.Store(false)
		s.releaseRun()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = reg.Shutdown(sctx)
	})
	const id = "test.timedout-zombie-unreadable"
	def := makeValidDef(id)
	def.Run = s.run
	if err := reg.RegisterOp(def); err != nil {
		t.Fatal(err)
	}
	reg.Start(t.Context())
	opID, err := reg.EnqueueOp(t.Context(), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	recv(t, s.started, "run to start")
	if err := reg.Cancel(opID); err != nil {
		t.Fatal(err)
	}
	awaitZombieCount(t, reg, 1)
	if err := reg.Cancel(opID); err != nil {
		t.Fatal(err)
	}
	awaitStatus(t, store.fakeStore, opID, "canceled", 5*time.Second)
	store.failRead.Store(true)
	s.releaseRun()
	awaitZombieCount(t, reg, 0)
	store.failRead.Store(false)
	wantOutcomes(t, g, id, map[string]int64{"started": 1, "canceled": 1})
}

// TestShutdownTimeoutStampFailsDependents: a run stamped as a timeout by
// Shutdown is final, and its goroutine may never return to tell the
// dependency scheduler, so Shutdown tells it: a dependent waiting on it is
// failed, not left in waiting_deps.
func TestShutdownTimeoutStampFailsDependents(t *testing.T) {
	store := newV3Store(t)
	s := newStubbornRun()
	r := registry.NewWithOptions(store, slog.Default(), 2, registry.Options{
		AbandonGrace: time.Second, WatchdogInterval: time.Hour, SweepInterval: time.Hour, AbandonedCap: 100,
	})
	t.Cleanup(func() {
		s.releaseRun()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = r.Shutdown(sctx)
	})
	defA := makeValidDef("test.timedout-dep-a")
	defA.ResumePolicy = registry.ResumeRestart
	defA.Timeout = 20 * time.Millisecond
	defA.Run = s.run
	defB := makeValidDef("test.timedout-dep-b")
	defB.Requires = []registry.Requirement{{Kind: registry.ReqOpCompleted, OpType: defA.ID}}
	for _, d := range []registry.OperationDef{defA, defB} {
		if err := r.RegisterOp(d); err != nil {
			t.Fatal(err)
		}
	}
	r.SetDepsScheduler(registry.NewDepsScheduler(r, &pebbleSchedulerStore{PebbleStore: store}))
	reqs, _ := json.Marshal(defB.Requires)
	waiting := ulid.Make().String()
	if err := store.InsertOperationV2(database.OperationV2Row{ID: waiting, DefID: defB.ID, Plugin: "test", Status: "waiting_deps",
		QueuedAt: time.Now().UTC(), SubjectType: "book", SubjectID: "book-77", Requirements: string(reqs), Params: `{"book_id":"book-77"}`}); err != nil {
		t.Fatal(err)
	}
	r.Start(t.Context())
	a, err := r.EnqueueOp(t.Context(), defA.ID, map[string]any{"book_id": "book-77"})
	if err != nil {
		t.Fatal(err)
	}
	recv(t, s.started, "run to start")
	time.Sleep(150 * time.Millisecond) // past the deadline, inside the 1s grace
	sctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = r.Shutdown(sctx)
	row, err := store.GetOperationV2(a)
	if err != nil || row == nil || row.Status != "canceled" || row.ErrorMessage == nil || !strings.HasPrefix(*row.ErrorMessage, state.TimeoutPrefix) {
		t.Fatalf("prerequisite after Shutdown = %+v %v, want canceled with a %q message", row, err, state.TimeoutPrefix)
	}
	if got := v2Status(t, store, waiting); got != "failed" {
		t.Fatalf("dependent after Shutdown = %q, want failed", got)
	}
	s.releaseRun()
	recv(t, s.finished, "the stubborn run to return")
}

// TestShutdownStampsATimedOutSubprocessRunAsATimeout: the subprocess
// (Isolate=true) path sets deadlineHit too. Its child is killed at the
// deadline, but a grandchild holds the result socket open past Shutdown's
// goroutine wait, so the run is still in flight when Shutdown's context
// expires and when Shutdown returns; Shutdown stamps it as a timeout, not
// interrupted_quiesced, and counts it timed_out.
func TestShutdownStampsATimedOutSubprocessRunAsATimeout(t *testing.T) {
	prev := registry.ChildEnvFunc
	registry.ChildEnvFunc = func() []string {
		return []string{testChildEnvVar + "=1", testChildHangVar + "=4500"}
	}
	t.Cleanup(func() { registry.ChildEnvFunc = prev })

	rec, g := privateMeter()
	store := newFakeStore()
	r := registry.NewWithOptions(store, slog.Default(), 1, registry.Options{OpsMetrics: rec})
	g.reg = r
	t.Cleanup(func() {
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = r.Shutdown(sctx)
	})
	const id = "test.timedout-subprocess"
	def := makeValidDef(id)
	def.Isolate = true
	def.ResumePolicy = registry.ResumeRestart
	def.Timeout = time.Second
	def.Run = func(context.Context, json.RawMessage, registry.Reporter) error {
		t.Error("def.Run should not be called in-process for Isolate=true")
		return nil
	}
	if err := r.RegisterOp(def); err != nil {
		t.Fatal(err)
	}
	r.Start(t.Context())
	opID, err := r.EnqueueOp(t.Context(), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	awaitStatus(t, store, opID, "running", 5*time.Second)
	time.Sleep(1300 * time.Millisecond) // past the 1s deadline; the grandchild holds the socket
	sctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_ = r.Shutdown(sctx)
	row, err := store.GetOperationV2(opID)
	if err != nil || row == nil {
		t.Fatalf("GetOperationV2: %v %v", row, err)
	}
	if row.Status != "canceled" || row.ErrorMessage == nil || !strings.HasPrefix(*row.ErrorMessage, state.TimeoutPrefix) {
		t.Fatalf("row after Shutdown = %q %v, want canceled with a %q message", row.Status, row.ErrorMessage, state.TimeoutPrefix)
	}
	wantOutcomes(t, g, id, map[string]int64{"started": 1, "timed_out": 1})
	// The run ends when the grandchild lets go; it must not count again.
	deadline := time.Now().Add(10 * time.Second)
	for r.IsRunning(opID) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if r.IsRunning(opID) {
		t.Fatal("the subprocess run never ended")
	}
	wantOutcomes(t, g, id, map[string]int64{"started": 1, "timed_out": 1})
}
